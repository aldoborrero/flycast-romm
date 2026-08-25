package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aldoborrero/flycast-romm/internal/config"
	"github.com/aldoborrero/flycast-romm/internal/flycast"
	"github.com/aldoborrero/flycast-romm/internal/session"
)

// ── Fake controller ──────────────────────────────────────────────────────────

type call struct {
	Op   string
	Str  string
	Int  int
	Bool *bool
}

type fakeController struct {
	mu    sync.Mutex
	calls []call
	seen  chan call

	launchErr error
	saveErr   error
	loadErr   error
	volumeErr error
	muteErr   error
	muteState bool
	ready     bool
	probe     flycast.Probe
}

func newFake() *fakeController {
	return &fakeController{seen: make(chan call, 32), ready: true}
}

func (f *fakeController) record(c call) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	select {
	case f.seen <- c:
	default:
	}
}

func (f *fakeController) Launch(_ context.Context, romPath string) (flycast.LaunchResult, error) {
	f.record(call{Op: "launch", Str: romPath})
	if f.launchErr != nil {
		return flycast.LaunchResult{}, f.launchErr
	}
	return flycast.LaunchResult{ROMPath: romPath, Ready: f.ready}, nil
}

func (f *fakeController) SaveState(_ context.Context, romPath string, slot int) error {
	f.record(call{Op: "save", Str: romPath, Int: slot})
	return f.saveErr
}

func (f *fakeController) LoadState(_ context.Context, slot int) error {
	f.record(call{Op: "load", Int: slot})
	return f.loadErr
}

func (f *fakeController) Kill(context.Context) error {
	f.record(call{Op: "kill"})
	return nil
}

func (f *fakeController) SetVolume(_ context.Context, level int) error {
	f.record(call{Op: "volume", Int: level})
	return f.volumeErr
}

func (f *fakeController) SetMute(_ context.Context, mute *bool) (bool, error) {
	f.record(call{Op: "mute", Bool: mute})
	if f.muteErr != nil {
		return false, f.muteErr
	}
	if mute != nil {
		f.muteState = *mute
	} else {
		f.muteState = !f.muteState
	}
	return f.muteState, nil
}

func (f *fakeController) Probe() flycast.Probe { return f.probe }

// await blocks for the next call of the given op, so tests can observe work
// the handler dispatched to a goroutine.
func (f *fakeController) await(t *testing.T, op string) call {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-f.seen:
			if c.Op == op {
				return c
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q call; saw %+v", op, f.snapshot())
			return call{}
		}
	}
}

func (f *fakeController) snapshot() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeController) count(op string) int {
	n := 0
	for _, c := range f.snapshot() {
		if c.Op == op {
			n++
		}
	}
	return n
}

// ── Harness ──────────────────────────────────────────────────────────────────

type harness struct {
	t    *testing.T
	srv  *httptest.Server
	fake *fakeController
	sess *session.Manager
	cfg  config.Config
	root string
}

const testSecret = "s3cret"

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()

	cfg := config.Config{
		Secret:   testSecret,
		ROMRoot:  root,
		SaveSlot: config.MaxSlot,
		StateDir: t.TempDir(),
	}
	fake := newFake()
	sess := session.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := httptest.NewServer(NewServer(cfg, log, fake, sess).Handler())
	t.Cleanup(srv.Close)

	return &harness{t: t, srv: srv, fake: fake, sess: sess, cfg: cfg, root: root}
}

func (h *harness) rom(rel string) string {
	h.t.Helper()
	path := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *harness) do(method, path, body string, withSecret bool) (int, map[string]any) {
	h.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if withSecret {
		req.Header.Set("X-Broker-Secret", testSecret)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("%s %s returned non-JSON %q", method, path, raw)
		}
	}
	return resp.StatusCode, out
}

func (h *harness) post(path, body string) (int, map[string]any) {
	return h.do(http.MethodPost, path, body, true)
}

// launch puts a game in the session, which most control routes require.
func (h *harness) launch(rel string) string {
	h.t.Helper()
	rom := h.rom(rel)
	code, _ := h.post("/launch", `{"rom_path":`+quote(rom)+`,"rom_name":"whatever"}`)
	if code != http.StatusOK {
		h.t.Fatalf("setup launch returned %d", code)
	}
	h.fake.await(h.t, "launch")
	return rom
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ── Auth ─────────────────────────────────────────────────────────────────────

func TestSecretIsRequired(t *testing.T) {
	h := newHarness(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/status"},
		{http.MethodPost, "/launch"},
		{http.MethodDelete, "/launch"},
		{http.MethodPost, "/save-and-exit"},
		{http.MethodPost, "/save-state"},
		{http.MethodPost, "/load-state"},
		{http.MethodPost, "/volume"},
		{http.MethodPost, "/mute"},
	} {
		code, body := h.do(route.method, route.path, "", false)
		if code != http.StatusForbidden {
			t.Errorf("%s %s without a secret = %d, want 403", route.method, route.path, code)
		}
		if body["error"] != "forbidden" {
			t.Errorf("%s %s error = %v, want forbidden", route.method, route.path, body["error"])
		}
	}
}

// A container healthcheck cannot carry the shared secret.
func TestHealthNeedsNoSecret(t *testing.T) {
	h := newHarness(t)
	h.fake.probe = flycast.Probe{BinaryPresent: true, DisplayUp: true, LuaReady: true}

	code, body := h.do(http.MethodGet, "/health", "", false)
	if code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", code)
	}
	for _, key := range []string{"status", "flycast_installed", "display_up", "lua_ready", "session_active"} {
		if _, ok := body[key]; !ok {
			t.Errorf("GET /health is missing %q: %v", key, body)
		}
	}
	if body["status"] != "ok" {
		t.Errorf("GET /health status = %v, want ok", body["status"])
	}
}

// ── Launch ───────────────────────────────────────────────────────────────────

func TestLaunchResolvesAndClaims(t *testing.T) {
	h := newHarness(t)
	rom := h.rom("dc/Crazy Taxi.chd")

	code, body := h.post("/launch", `{"rom_path":`+quote(rom)+`,"rom_name":"Crazy Taxi"}`)
	if code != http.StatusOK {
		t.Fatalf("POST /launch = %d (%v)", code, body)
	}
	if body["rom_path"] != rom {
		t.Errorf("rom_path = %v, want %q", body["rom_path"], rom)
	}
	if c := h.fake.await(t, "launch"); c.Str != rom {
		t.Errorf("controller launched %q, want %q", c.Str, rom)
	}
	if snap := h.sess.Snapshot(); !snap.Active || snap.ROMName != "Crazy Taxi" {
		t.Errorf("session after launch = %+v", snap)
	}
}

// RomM sends rom_name, and every broker in this family derives the display
// name from the file it actually resolved instead.
func TestLaunchIgnoresRomNameForFolders(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.root, "dc", "Shenmue")
	h.rom("dc/Shenmue/Shenmue (Disc 1).gdi")

	code, body := h.post("/launch", `{"rom_path":`+quote(dir)+`,"rom_name":"Shenmue"}`)
	if code != http.StatusOK {
		t.Fatalf("POST /launch = %d (%v)", code, body)
	}
	if got, _ := body["rom_path"].(string); filepath.Base(got) != "Shenmue (Disc 1).gdi" {
		t.Fatalf("rom_path = %v, want the resolved disc image", body["rom_path"])
	}
	if body["rom_name"] != "Shenmue (Disc 1)" {
		t.Errorf("rom_name = %v, want the resolved file's name", body["rom_name"])
	}
}

func TestLaunchRefusesPathsOutsideTheRoot(t *testing.T) {
	h := newHarness(t)
	outside := filepath.Join(t.TempDir(), "evil.chd")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := h.post("/launch", `{"rom_path":`+quote(outside)+`}`)
	if code != http.StatusBadRequest {
		t.Fatalf("POST /launch outside the root = %d, want 400", code)
	}
	if body["rom_root"] != h.root {
		t.Errorf("body is missing the rom_root hint: %v", body)
	}
	if h.fake.count("launch") != 0 {
		t.Error("the controller was asked to launch a path outside the root")
	}
}

// The usual cause is the library being mounted at a different path here than
// in the RomM container, which is why the message says so rather than 404.
func TestLaunchMissingPathIs422(t *testing.T) {
	h := newHarness(t)
	code, _ := h.post("/launch", `{"rom_path":`+quote(filepath.Join(h.root, "dc/nope.chd"))+`}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /launch of a missing file = %d, want 422", code)
	}
}

func TestLaunchUnbootableFolderListsExtensions(t *testing.T) {
	h := newHarness(t)
	h.rom("dc/Empty/readme.txt")

	code, body := h.post("/launch", `{"rom_path":`+quote(filepath.Join(h.root, "dc/Empty"))+`}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /launch of an unbootable folder = %d, want 422", code)
	}
	exts, ok := body["extensions"].([]any)
	if !ok || len(exts) == 0 {
		t.Fatalf("422 is missing the extensions list: %v", body)
	}
}

func TestLaunchRequiresRomPath(t *testing.T) {
	h := newHarness(t)
	for _, b := range []string{`{}`, `{"rom_path":""}`, `{"rom_path":"   "}`} {
		if code, _ := h.post("/launch", b); code != http.StatusBadRequest {
			t.Errorf("POST /launch %s = %d, want 400", b, code)
		}
	}
}

// A typo'd body must be an error, not a silently different action.
func TestMalformedJSONIsRejected(t *testing.T) {
	h := newHarness(t)
	for _, b := range []string{`{"rom_path":`, `[1,2,3]`, `"a string"`} {
		if code, _ := h.post("/launch", b); code != http.StatusBadRequest {
			t.Errorf("POST /launch %s = %d, want 400", b, code)
		}
	}
}

func TestStopReturnsToTheGameList(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")

	code, body := h.do(http.MethodDelete, "/launch", "", true)
	if code != http.StatusOK || body["status"] != "resetting" {
		t.Fatalf("DELETE /launch = %d %v", code, body)
	}
	h.fake.await(t, "kill")
	if c := h.fake.await(t, "launch"); c.Str != "" {
		t.Errorf("after DELETE the controller launched %q, want the idle game list", c.Str)
	}
	if h.sess.Snapshot().Active {
		t.Error("DELETE /launch left the session active")
	}
}

// ── Save states ──────────────────────────────────────────────────────────────

// RomM checks this literal string, not the status code.
func TestSaveStateAnswersSavingAndDispatches(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")

	code, body := h.post("/save-state", `{"slot":3}`)
	if code != http.StatusOK {
		t.Fatalf("POST /save-state = %d (%v)", code, body)
	}
	if body["status"] != "saving" {
		t.Fatalf("status = %v, want the literal \"saving\" RomM checks for", body["status"])
	}
	// RomM slot 3 is Flycast index 2.
	if c := h.fake.await(t, "save"); c.Int != 2 {
		t.Errorf("controller saved flycast slot %d, want 2", c.Int)
	}
}

func TestSaveStateNeedsAGame(t *testing.T) {
	h := newHarness(t)
	code, body := h.post("/save-state", `{"slot":1}`)
	if code != http.StatusConflict {
		t.Fatalf("POST /save-state with no game = %d, want 409", code)
	}
	if body["error"] != session.ErrNoGame.Error() {
		t.Errorf("error = %v, want %q", body["error"], session.ErrNoGame)
	}
}

func TestSaveStateRejectsOutOfRangeSlots(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")

	for _, b := range []string{`{"slot":11}`, `{"slot":-1}`, `{"slot":"three"}`} {
		if code, _ := h.post("/save-state", b); code != http.StatusBadRequest {
			t.Errorf("POST /save-state %s = %d, want 400", b, code)
		}
	}
}

func TestLoadStateMapsTheAutosaveSlot(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")

	code, body := h.post("/load-state", `{"slot":10}`)
	if code != http.StatusOK {
		t.Fatalf("POST /load-state = %d (%v)", code, body)
	}
	if body["loaded"] != true {
		t.Errorf("loaded = %v, want true", body["loaded"])
	}
	// RomM slot 10, the autosave slot, is Flycast index 9.
	if c := h.fake.await(t, "load"); c.Int != 9 {
		t.Errorf("controller loaded flycast slot %d, want 9", c.Int)
	}
}

func TestLoadStateFailureIsNotSilent(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")
	h.fake.loadErr = errors.New("lua channel did not answer")

	code, body := h.post("/load-state", `{"slot":1}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("POST /load-state after a controller error = %d, want 500", code)
	}
	// RomM turns a falsy `loaded` into a 502 for the user.
	if body["loaded"] != false {
		t.Errorf("loaded = %v, want false", body["loaded"])
	}
}

// ── Save and exit ────────────────────────────────────────────────────────────

func TestSaveAndExitDefaultsToTheAutosaveSlot(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")

	code, body := h.post("/save-and-exit", `{"slot":0,"wait":true}`)
	if code != http.StatusOK {
		t.Fatalf("POST /save-and-exit = %d (%v)", code, body)
	}
	if body["saved"] != true {
		t.Errorf("saved = %v, want true", body["saved"])
	}
	// Slot 0 means the autosave slot, RomM 10, which is Flycast index 9.
	if c := h.fake.await(t, "save"); c.Int != 9 {
		t.Errorf("controller saved flycast slot %d, want 9", c.Int)
	}
	h.fake.await(t, "kill")
	if h.sess.Snapshot().Active {
		t.Error("save-and-exit left the session active")
	}
}

// The user asked to leave. Refusing to exit because the save failed would
// strand the session and wedge the container.
func TestSaveAndExitStillExitsWhenTheSaveFails(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")
	h.fake.saveErr = errors.New("state never settled")

	code, body := h.post("/save-and-exit", `{}`)
	if code != http.StatusOK {
		t.Fatalf("POST /save-and-exit = %d (%v)", code, body)
	}
	if body["saved"] != false {
		t.Errorf("saved = %v, want false", body["saved"])
	}
	h.fake.await(t, "kill")
}

func TestSaveAndExitWithoutWaitingIsQueued(t *testing.T) {
	h := newHarness(t)
	h.launch("dc/game.chd")

	code, body := h.post("/save-and-exit", `{"slot":0,"wait":false}`)
	if code != http.StatusOK {
		t.Fatalf("POST /save-and-exit = %d (%v)", code, body)
	}
	if body["status"] != "queued" {
		t.Errorf("status = %v, want queued", body["status"])
	}
	h.fake.await(t, "save")
	h.fake.await(t, "kill")
}

func TestSaveAndExitNeedsAGame(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.post("/save-and-exit", `{}`); code != http.StatusConflict {
		t.Fatalf("POST /save-and-exit with no game = %d, want 409", code)
	}
}

// ── Volume and mute ──────────────────────────────────────────────────────────

// RomM checks this literal string, not the status code.
func TestVolume(t *testing.T) {
	h := newHarness(t)

	code, body := h.post("/volume", `{"level":42}`)
	if code != http.StatusOK {
		t.Fatalf("POST /volume = %d (%v)", code, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %v, want the literal \"ok\" RomM checks for", body["status"])
	}
	if body["level"] != float64(42) {
		t.Errorf("level = %v, want 42", body["level"])
	}
	if c := h.fake.await(t, "volume"); c.Int != 42 {
		t.Errorf("controller got level %d, want 42", c.Int)
	}
}

func TestVolumeRange(t *testing.T) {
	h := newHarness(t)
	for _, b := range []string{`{"level":101}`, `{"level":-1}`, `{"level":"loud"}`, `{}`} {
		if code, _ := h.post("/volume", b); code != http.StatusBadRequest {
			t.Errorf("POST /volume %s = %d, want 400", b, code)
		}
	}
}

func TestVolumeFailureIs500(t *testing.T) {
	h := newHarness(t)
	h.fake.volumeErr = errors.New("pactl: connection refused")

	if code, _ := h.post("/volume", `{"level":50}`); code != http.StatusInternalServerError {
		t.Fatalf("POST /volume after a pactl failure = %d, want 500", code)
	}
}

// An absent `mute` key means toggle; that is how RomM's UI asks for one.
func TestMuteTogglesWithoutABody(t *testing.T) {
	h := newHarness(t)

	code, body := h.post("/mute", `{}`)
	if code != http.StatusOK {
		t.Fatalf("POST /mute = %d (%v)", code, body)
	}
	if body["mute"] != true {
		t.Errorf("mute = %v, want the toggled state", body["mute"])
	}
	if c := h.fake.await(t, "mute"); c.Bool != nil {
		t.Errorf("controller got an explicit %v, want a toggle", *c.Bool)
	}
}

func TestMuteSetsExplicitly(t *testing.T) {
	h := newHarness(t)

	_, body := h.post("/mute", `{"mute":true}`)
	if body["mute"] != true {
		t.Errorf("mute = %v, want true", body["mute"])
	}
	c := h.fake.await(t, "mute")
	if c.Bool == nil || !*c.Bool {
		t.Errorf("controller got %v, want an explicit true", c.Bool)
	}

	_, body = h.post("/mute", `{"mute":false}`)
	if body["mute"] != false {
		t.Errorf("mute = %v, want false", body["mute"])
	}
}

// ── Status ───────────────────────────────────────────────────────────────────

func TestStatusReportsTheLoadedGame(t *testing.T) {
	h := newHarness(t)

	_, body := h.do(http.MethodGet, "/status", "", true)
	if body["active"] != false || body["rom_path"] != nil {
		t.Fatalf("idle /status = %v", body)
	}
	if body["autosave_slot"] != float64(config.MaxSlot) {
		t.Errorf("autosave_slot = %v, want %d", body["autosave_slot"], config.MaxSlot)
	}

	rom := h.launch("dc/game.chd")
	_, body = h.do(http.MethodGet, "/status", "", true)
	if body["active"] != true || body["rom_path"] != rom {
		t.Fatalf("/status after a launch = %v", body)
	}
	if body["started_at"] == nil {
		t.Error("/status is missing started_at")
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do(http.MethodGet, "/nope", "", true); code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", code)
	}
}
