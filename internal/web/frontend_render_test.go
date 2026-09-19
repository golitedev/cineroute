package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrontendSubtitlesRender runs the page's own JavaScript against a stub DOM
// with representative API payloads. Two bugs reached a running deployment that
// every Go test passed through: the subtitles block was nested inside login()
// (so its functions were never global), and a const was used before its
// declaration, which aborts the whole render. Both are only detectable by
// executing the script. The test is skipped when node is not installed.
func TestFrontendSubtitlesRender(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the page script smoke test")
	}
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	script := extractInlineScript(t, string(data))
	bootstrap := "loadStatus();loadHistory();refresh();"
	index := strings.LastIndex(script, bootstrap)
	if index < 0 {
		t.Fatalf("bootstrap call %q not found in the page script", bootstrap)
	}

	dir := t.TempDir()
	headPath := filepath.Join(dir, "page.js")
	driverPath := filepath.Join(dir, "driver.js")
	if err := os.WriteFile(headPath, []byte(script[:index]), 0o644); err != nil {
		t.Fatalf("write page script: %v", err)
	}
	if err := os.WriteFile(driverPath, []byte(frontendDriver), 0o644); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	cmd := exec.Command(node, driverPath, headPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("page script failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "RENDER_OK") {
		t.Fatalf("page script did not complete:\n%s", output)
	}
}

// frontendDriver is the node program: it stubs just enough DOM for the page
// script, then renders the Subtitles view in several states and fails loudly on
// the first broken expectation.
const frontendDriver = `
const fs = require("fs"), vm = require("vm");
const script = fs.readFileSync(process.argv[2], "utf8");

function makeEl() {
  return {
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    style: {}, dataset: {}, value: "", textContent: "", innerHTML: "", disabled: false,
    isConnected: true, checked: false, files: [], placeholder: "", title: "",
    addEventListener() {}, removeEventListener() {}, appendChild() {}, removeChild() {},
    scrollIntoView() {}, focus() {}, select() {}, click() {}, setAttribute() {},
    getAttribute() { return null; }, querySelectorAll() { return []; },
    querySelector() { return null; }, insertBefore() {}, closest() { return null; },
  };
}
const elements = new Map();
const document = {
  getElementById(id) { if (!elements.has(id)) elements.set(id, makeEl()); return elements.get(id); },
  querySelectorAll() { return []; }, query() { return null; }, createElement() { return makeEl(); },
  addEventListener() {}, activeElement: null, body: makeEl(),
};
const sandbox = {
  document, console, alert(message) { console.log("ALERT: " + message); }, confirm() { return true; },
  fetch: async () => ({ ok: true, status: 200, json: async () => ({}) }),
  setTimeout() { return 0; }, clearTimeout() {}, setInterval() { return 0; }, clearInterval() {},
  Number, String, Math, JSON, Object, Array, Date, parseFloat, parseInt, isFinite, Intl, Symbol, Error, RegExp, Map, Set,
};
sandbox.window = sandbox; sandbox.self = sandbox; sandbox.globalThis = sandbox; sandbox.location = { href: "" };

function fail(message) { console.log("FAILURE: " + message); process.exit(1); }
function expect(condition, message) { if (!condition) fail(message); }

const item = {
  id: "s_1", drive_id: "hdd1", folder_name: "Hugo (2011)", video_name: "Hugo.2011.1080p.mkv",
  video_path: "/hdd1/movies-remote/Hugo (2011)/Hugo.2011.1080p.mkv", title: "Hugo", year: 2011,
  status: "pending", probed: true, has_swedish: false, has_external_subtitle: false,
  existing_sub_languages: ["es", "en"], external_subtitles: [], embedded_sub_streams: [{ index: 3, codec: "subrip", language: "es", usable: true }],
  attempts: 0,
};
const view = {
  enabled: true, work_dir: "/data/subtitles-work", target_language: "sv", reference_languages: ["en", "es"], accept: {},
  settings: { run_batch_size: 20, request_interval_ms: 400, max_candidates: 5, alass_split_penalty: 7, quota_reserve: 5 },
  stats: { total: 729, pending: 94, added: 1, has_swedish: 633, skipped: 1, not_analyzed: 58, no_external_subtitle: 94 },
  items: [item], batch: { running: false, total: 0, done: 0 }, quota: { known: false },
};

const driver = [];
driver.push("try {");
driver.push("subtitleData = " + JSON.stringify(view) + ";");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleRunButton').textContent.includes('next 20'), 'idle batch button should name the batch: ' + $('subtitleRunButton').textContent);");
driver.push("expect($('subtitleProgress').textContent.includes('next batch: 20 of 94'), 'idle hint missing: ' + $('subtitleProgress').textContent);");
driver.push("expect($('subtitleStats').innerHTML.includes('729'), 'stats not rendered');");
driver.push("expect($('subtitleList').innerHTML.includes('Hugo'), 'movie row not rendered');");
driver.push("subtitleData.batch = { running: true, kind: 'run', total: 20, done: 3, stage: 'reference', current: '/hdd4/movies-remote/The Dark Knight (2008)/The.Dark.Knight.2008.1080p.mkv' };");
driver.push("subtitleData.items[0].status = 'processing';");
driver.push("subtitleData.items[0].step = 'reference';");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleRunButton').disabled && $('subtitleRunButton').textContent === 'Running…', 'running button state wrong');");
driver.push("expect($('subtitleProgress').innerHTML.includes('run-progress'), 'progress bar missing');");
driver.push("expect($('subtitleProgress').innerHTML.includes('Processing 3 / 20'), 'progress text missing');");
driver.push("expect($('subtitleList').innerHTML.includes('hardlink-card running'), 'running row not highlighted');");
driver.push("['all','no_external','has_external','not_analyzed','added','no_reference','has_swedish','skipped'].forEach(f => { setSubtitleFilter(f); });");
driver.push("subtitleData.open_item = subtitleData.items[0];");
driver.push("subtitleData.attempts = [{ file_id: 7, status: 'rejected_timing', release: 'Hugo.2011.WEB-DL', at: '2026-01-01T00:00:00Z', metrics: { within_2s: 0.5, p90: 4.2, score: 10 } }];");
driver.push("subtitleData.candidates = [{ rank: 1, file_id: 7, score: 420, category: 'strong', safe: true, release: 'Hugo.2011.WEB-DL', reasons: ['year match'] }];");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleDetail').innerHTML.includes('Attempts'), 'detail panel not rendered');");
driver.push("subtitleData.batch = { running: false, total: 0, done: 0 }; subtitleData.stats.pending = 0;");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleRunButton').disabled, 'batch button should be disabled with nothing queued');");
driver.push("console.log('RENDER_OK');");
driver.push("} catch (error) { fail(error && error.stack ? error.stack : String(error)); }");

sandbox.expect = expect;
sandbox.fail = fail;
vm.createContext(sandbox);
vm.runInContext(script + "\n" + driver.join("\n"), sandbox, { filename: "page.js" });
`
