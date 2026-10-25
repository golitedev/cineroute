package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/config"
	"cineroute/internal/requests"
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
	requestScript, err := os.ReadFile("templates/requests.js")
	if err != nil {
		t.Fatal(err)
	}
	catalogScript, err := os.ReadFile("templates/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	script = string(requestScript) + "\n" + string(catalogScript) + "\n" + script
	bootstrap := "loadSession();"
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
	fixture, err := json.Marshal(map[string]any{"settings": requests.Defaults(), "qualities": requests.QualityOptions, "codecs": requests.CodecOptions, "drives": []config.Drive{{ID: "disk1", MovieRoot: "/disk1/media/movies", MovieRemoteRoot: "/disk1/media/movies-remote", TVRoot: "/disk1/media/tv", TVRemoteRoot: "/disk1/media/tv-remote"}}, "prowlarr_configured": true, "engine_available": false})
	if err != nil {
		t.Fatal(err)
	}
	driverSource := strings.ReplaceAll(frontendDriver, "__REQUEST_SETTINGS_FIXTURE__", string(fixture))
	if err := os.WriteFile(driverPath, []byte(driverSource), 0o644); err != nil {
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
  const classes=new Set();
  return {
    classList: { add(...names) {names.forEach(name=>classes.add(name))}, remove(...names) {names.forEach(name=>classes.delete(name))}, toggle(name,on) {if(on===undefined)on=!classes.has(name);if(on)classes.add(name);else classes.delete(name)}, contains(name) { return classes.has(name); } },
    style: {}, dataset: {}, value: "", textContent: "", innerHTML: "", disabled: false,
    isConnected: true, checked: false, files: [], placeholder: "", title: "",
    addEventListener() {}, removeEventListener() {}, appendChild() {}, removeChild() {},
    scrollIntoView() {}, focus() {}, select() {}, click() {}, setAttribute() {},
    reportValidity() { return true; }, getAttribute() { return null; }, querySelectorAll() { return []; },
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
  AbortController, Number, String, Math, JSON, Object, Array, Date, parseFloat, parseInt, isFinite, Intl, Symbol, Error, RegExp, Map, Set,
};
sandbox.window = sandbox; sandbox.self = sandbox; sandbox.globalThis = sandbox; sandbox.location = { href: "" };

function fail(message) { console.log("FAILURE: " + message); process.exit(1); }
function expect(condition, message) { if (!condition) fail(message); }
function checkHandlers(html) { for(const match of html.matchAll(/on(?:click|input|change)="([^"]*)"/g)) { new vm.Script('(function(event){'+match[1]+'})'); } }

const item = {
  id: "s_1", drive_id: "disk1", folder_name: "Hugo (2011)", video_name: "Hugo.2011.1080p.mkv",
  video_path: "/disk1/media/movies-remote/Hugo (2011)/Hugo.2011.1080p.mkv", title: "Hugo", year: 2011,
  status: "pending", probed: true, has_external_subtitle: false,
  targets: [{ language: "sv", status: "pending" }, { language: "es", status: "present", present: true, sources: ["embedded stream #2"] }, { language: "en", status: "no_match", error: "no safe candidate was found" }],
  existing_sub_languages: ["es", "en"], external_subtitles: [], embedded_sub_streams: [{ index: 3, codec: "subrip", language: "es", usable: true }],
  attempts: 0,
};
const view = {
  enabled: true, work_dir: "/data/subtitles-work", target_languages: ["sv", "es", "en"], reference_languages: ["en", "es"], accept: {},
  settings: { run_batch_size: 20, request_interval_ms: 400, max_candidates: 5, alass_split_penalty: 7, quota_reserve: 5 },
  stats: { total: 729, pending: 94, added: 1, partial: 2, has_targets: 633, skipped: 1, not_analyzed: 58, no_external_subtitle: 94 },
  items: [item], batch: { running: false, total: 0, done: 0 }, quota: { known: false },
};

const driver = [];
driver.push("(async () => { try {");
driver.push("subtitleData = " + JSON.stringify(view) + ";");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleRunButton').textContent.includes('next 20'), 'idle batch button should name the batch: ' + $('subtitleRunButton').textContent);");
driver.push("expect($('subtitleProgress').textContent.includes('next batch: 20 of 94'), 'idle hint missing: ' + $('subtitleProgress').textContent);");
driver.push("expect($('subtitleStats').innerHTML.includes('729'), 'stats not rendered');");
driver.push("expect($('subtitleList').innerHTML.includes('Hugo'), 'movie row not rendered');");
driver.push("subtitleData.batch = { running: true, kind: 'run', total: 20, done: 3, stage: 'reference', current: '/disk4/media/movies-remote/The Dark Knight (2008)/The.Dark.Knight.2008.1080p.mkv' };");
driver.push("subtitleData.items[0].status = 'processing';");
driver.push("subtitleData.items[0].step = 'reference';");
driver.push("subtitleData.items[0].step_detail = '42% · 0:50:00 of 2:00:00';");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleRunButton').disabled && $('subtitleRunButton').textContent === 'Running…', 'running button state wrong');");
driver.push("expect($('subtitleProgress').innerHTML.includes('run-progress'), 'progress bar missing');");
driver.push("expect($('subtitleProgress').innerHTML.includes('Processing 3 / 20'), 'progress text missing');");
driver.push("expect($('subtitleList').innerHTML.includes('hardlink-card running'), 'running row not highlighted');");
driver.push("expect($('subtitleList').innerHTML.includes('no safe candidate was found'), 'per-language outcome is not shown on the row');");
driver.push("expect($('subtitleList').innerHTML.includes('embedded stream #2'), 'a language that is already present is not shown');");
driver.push("expect($('subtitleDescription').textContent.includes('sv, es, en'), 'the page does not name the target languages');");
driver.push("expect($('subtitleList').innerHTML.includes('42% · 0:50:00 of 2:00:00'), 'extraction progress is not shown while a movie is processed');");
driver.push("['all','no_external','has_external','not_analyzed','added','partial','no_reference','has_targets','skipped'].forEach(f => { setSubtitleFilter(f); });");
driver.push("subtitleData.open_item = subtitleData.items[0];");
driver.push("subtitleData.attempts = [{ file_id: 7, status: 'rejected_timing', release: 'Hugo.2011.WEB-DL', at: '2026-01-01T00:00:00Z', metrics: { within_2s: 0.5, p90: 4.2, score: 10 } }];");
driver.push("subtitleData.candidates = [{ rank: 1, file_id: 7, score: 420, category: 'strong', safe: true, release: 'Hugo.2011.WEB-DL', reasons: ['year match'] }];");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleDetail').innerHTML.includes('Attempts'), 'detail panel not rendered');");
driver.push("subtitleData.batch = { running: false, total: 0, done: 0 }; subtitleData.stats.pending = 0;");
driver.push("renderSubtitles();");
driver.push("expect($('subtitleRunButton').disabled, 'batch button should be disabled with nothing queued');");
const copyItem = { id: "copy_1", drive_id: "disk1", folder_name: "Hugo (2011)", path: "/disk1/media/movies/Hugo (2011)", remote_path: "/disk1/media/movies-remote/Hugo (2011)", status: "main_only", existing_files: ["Hugo.mkv"], existing_file_sizes: {"Hugo.mkv": 123}, remote_files: [], remote_file_sizes: {} };
driver.push("showView('copies',false,'movie');");
driver.push("copyData = " + JSON.stringify({items:[copyItem]}) + "; openCopy='copy_1'; renderCopies();");
driver.push("expect($('copyTitle').textContent === 'Movies', 'movie view title wrong');");
driver.push("expect($('copyList').innerHTML.includes('Hugo (2011)'), 'movie library row missing');");
driver.push("expect($('copyDetail').innerHTML.includes('Hardlink to remote'), 'main-only hardlink action missing');");
driver.push("setCopyFilter('remote_only'); expect(!$('copyList').innerHTML.includes('Hugo (2011)'), 'remote-only filter includes main-only movie');");
driver.push("showView('copies',false,'tv'); expect($('copyTitle').textContent === 'TV', 'TV view title wrong');");
driver.push("expect(!$('copyList').innerHTML.includes('Hugo (2011)'), 'movie inventory leaked into TV view');");
copyItem.status='remote_only'; copyItem.remote_files=copyItem.existing_files; copyItem.existing_files=[];
driver.push("copyData = " + JSON.stringify({items:[copyItem]}) + "; openCopy='copy_1'; renderCopies();");
driver.push("expect($('copyDetail').innerHTML.includes('Hardlink to main'), 'remote-only hardlink action missing');");
driver.push("copyData.items[0].status='both'; renderCopies(); expect(!$('copyDetail').innerHTML.includes('onclick=\"hardlinkCopy'), 'both-copy title offers a new hardlink');");
const requestFixture=__REQUEST_SETTINGS_FIXTURE__;
driver.push("currentAccount={name:'admin',role:'admin'};requestData="+JSON.stringify(requestFixture)+"; requestDraft=requestClone(requestData.settings);requestSavedJSON=JSON.stringify(requestDraft);renderRequests();checkHandlers($('requestsSettingsContent').innerHTML);");
driver.push("expect($('requestsSettingsContent').innerHTML.includes('Permissions and approval'),'request defaults not rendered');");
driver.push("setRequestField('movie.destination','main');setRequestField('movie.new_title_drive','disk1');setRequestPanel('movie');checkHandlers($('requestsSettingsContent').innerHTML);");
driver.push("expect(requestDraft.tv.destination==='remote','movie destination changed TV destination');");
driver.push("expect($('requestsSettingsContent').innerHTML.includes('value=\"main\" selected'), 'main destination is not selected');");
driver.push("expect($('requestsSettingsContent').innerHTML.includes('/disk1/media/movies</code>'),'selected main root missing');");
driver.push("moveRequestQuality('movie',1,-1);expect(requestDraft.movie.qualities[0]==='bluray-1080p','quality order not changed');");
driver.push("addRequestKeyword('movie');setRequestKeyword('movie',0,'pattern','DUAL <tag>');setRequestKeyword('movie',0,'mode','required');");
driver.push("expect(requestDraft.movie.keywords[0].score===0,'required keyword kept a preference score');");
driver.push("expect($('requestsSettingsContent').innerHTML.includes('DUAL &lt;tag&gt;'),'keyword pattern is not escaped');");
driver.push("setRequestPanel('tv');checkHandlers($('requestsSettingsContent').innerHTML);expect($('requestsSettingsContent').innerHTML.includes('Maximum season / series pack size'),'TV pack limit missing');");
driver.push("requestIndexers=[{id:3,name:'Tracker',enable:true,protocol:'torrent'},{id:4,name:'Usenet',enable:true,protocol:'usenet'},{id:5,name:'Disabled',enable:false,protocol:'torrent'}];setRequestPanel('trackers');checkHandlers($('requestsSettingsContent').innerHTML);toggleRequestTracker(3,true);toggleRequestTracker(4,true);toggleRequestTracker(5,true);setRequestTrackerPriority(3,2);");
driver.push("expect(requestDraft.trackers.length===1&&requestDraft.trackers[0].priority===2,'invalid tracker selected or priority lost');");
driver.push("requestIndexers=[];requestIndexersLoaded=true;requestIndexerError='Prowlarr unavailable';renderRequests();expect($('requestsSettingsContent').innerHTML.includes('Tracker')&&requestDraft.trackers.length===1,'offline refresh discarded selected tracker');");
driver.push("expect(requestDirty()&&requestDraft.movie.destination==='main','panel navigation discarded unsaved changes');");
driver.push("fetch=async(path,opts)=>({ok:true,status:200,json:async()=>({...requestData,settings:JSON.parse(opts.body)})});await saveRequestSettings();expect(!requestDirty(),'successful save left a dirty draft');");
driver.push("setRequestField('tv_limit',2);let resolveSave;fetch=async(path,opts)=>new Promise(resolve=>{resolveSave=()=>resolve({ok:true,status:200,json:async()=>({...requestData,settings:JSON.parse(opts.body)})})});const saving=saveRequestSettings();setRequestField('movie_limit',4);resolveSave();await saving;expect(requestDirty()&&requestDraft.movie_limit===4,'edits made during save were discarded');");
driver.push("setRequestField('movie_limit',5);fetch=async()=>({ok:false,status:400,json:async()=>({error:'Invalid settings'})});await saveRequestSettings();expect(requestDirty()&&requestDraft.movie_limit===5&&$('requestsMessage').textContent==='Invalid settings','failed save discarded changes or hid validation error');");
driver.push("currentAccount={name:'alice',role:'user'};portalMode=true;showApp();expect($('appShell').classList.contains('hidden')&&!$('userShell').classList.contains('hidden'),'user can see administration shell');");
driver.push("const movieResult={id:101,title:'Example Movie',media_type:'movie',poster_path:'/poster.jpg',release_date:'2024-01-01'};const showResult={id:202,name:'Example Show',media_type:'tv',poster_path:'/show.jpg'};let sentRequest=null;const pendingRequest={id:'own_request',media:showResult,status:'pending',message:'Waiting for administrator approval',progress:0,seasons:[1],follow_future:true};");
driver.push("fetch=async(path,opts)=>({ok:true,status:200,json:async()=>path.startsWith('/api/catalog/search')?{results:[movieResult,showResult],page:1,total_pages:1}:path.startsWith('/api/catalog/')?{media:showResult,seasons:[{season_number:1,name:'Season 1',episode_count:2,air_date:'2024-01-01'}],monitor:'requested_seasons',auto_approve:false}:path==='/api/user/requests'&&opts?.method==='POST'?(sentRequest=JSON.parse(opts.body),{request:pendingRequest}):{requests:[pendingRequest]}});$('catalogQuery').value='Example';await searchCatalog();checkHandlers($('catalogResults').innerHTML);expect($('catalogResults').innerHTML.includes('catalog-poster')&&$('catalogResults').innerHTML.includes('Example Show'),'movie/show poster search did not render');");
driver.push("await openCatalogTitle('tv',202);checkHandlers($('catalogDetail').innerHTML);expect($('catalogDetail').innerHTML.includes('Choose seasons')&&$('catalogDetail').innerHTML.includes('Request approval'),'TV season selection or approval state missing');");
driver.push("document.querySelectorAll=selector=>selector==='#catalogSeasons input:checked'?[{value:'1'}]:[];$('catalogFollowFuture').checked=true;await submitCatalogRequest();expect(sentRequest.tmdb_id===202&&sentRequest.seasons[0]===1&&sentRequest.follow_future&&!('destination' in sentRequest),'user request parameters are incorrect');expect(catalogJobs[0].status==='pending'&&$('myRequestList').innerHTML.includes('Awaiting approval'),'personal request status missing');checkHandlers($('myRequestList').innerHTML);");
driver.push("showLogin();expect(catalogJobs.length===0&&$('myRequestList').innerHTML===''&&$('appShell').classList.contains('hidden'),'sign-out/login boundary retained another account data');");
driver.push("console.log('RENDER_OK');");
driver.push("} catch (error) { fail(error && error.stack ? error.stack : String(error)); } })()");

sandbox.checkHandlers = checkHandlers;
sandbox.expect = expect;
sandbox.fail = fail;
vm.createContext(sandbox);
vm.runInContext(script + "\n" + driver.join("\n"), sandbox, { filename: "page.js" });
`
