// Administrator request policy and queue. Server permissions remain authoritative.
let requestData=null,requestDraft=null,requestSavedJSON="",requestPanel="general";
let requestIndexers=[],requestIndexersLoaded=false,requestIndexerError="",requestSaving=false,requestLoading=false;
const requestClone=value=>JSON.parse(JSON.stringify(value));
const requestDirty=()=>!!requestDraft&&JSON.stringify(requestDraft)!==requestSavedJSON;
const requestValue=path=>path.split('.').reduce((value,key)=>value[key],requestDraft);
function setRequestField(path,value){
  const keys=path.split('.'),key=keys.pop();
  keys.reduce((object,name)=>object[name],requestDraft)[key]=value;
  updateRequestSaveState();
}
function updateRequestSaveState(){
  $('requestsSaveButton').disabled=!requestDraft||requestSaving||requestLoading||!requestDirty();
  $('requestsReloadButton').disabled=requestSaving||requestLoading;
  $('requestsSaveState').textContent=requestSaving?'Saving…':requestDirty()?'Unsaved changes':requestDraft?'All changes saved':'';
}
function requestMessage(message,error=false){
  const el=$('requestsMessage');el.textContent=message;el.className=message?(error?'errbox':'okbox'):'';
}
async function loadRequests(force=false){
  if(currentAccount?.role!=="admin")return;const owner=currentAccount.name;
  if(requestLoading||requestSaving)return;
  if(requestData&&!force){renderRequests();return}
  if(force&&requestDirty()&&!confirm('Discard unsaved request settings and reload?'))return;
  requestLoading=true;$('requestsLoading').textContent='Loading request settings…';updateRequestSaveState();
  try{
    const data=await api('/api/requests/settings');
    if(currentAccount?.role!=="admin"||currentAccount.name!==owner)return;
    requestData=data;requestDraft=requestClone(data.settings);requestSavedJSON=JSON.stringify(requestDraft);
    renderRequests();requestMessage('');
    if(data.prowlarr_configured&&!requestIndexersLoaded)loadRequestIndexers();
  }catch(e){requestMessage(e.message,true)}
  finally{requestLoading=false;$('requestsLoading').textContent='';updateRequestSaveState()}
}
async function saveRequestSettings(){
  if(currentAccount?.role!=="admin")return;const owner=currentAccount.name;
  if(!requestDraft||requestSaving||requestLoading||!$('requestsForm').reportValidity())return;
  let snapshot;
  try{snapshot=JSON.stringify(requestDraft,(key,value)=>{if(typeof value==='number'&&!Number.isFinite(value))throw new Error('Fill in all numeric settings before saving.');return value})}catch(e){requestMessage(e.message,true);return}
  requestSaving=true;requestMessage('');updateRequestSaveState();
  try{
    const response=await api('/api/requests/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:snapshot});
    if(currentAccount?.role!=="admin"||currentAccount.name!==owner)return;
    requestData=response;requestSavedJSON=JSON.stringify(response.settings);
    if(JSON.stringify(requestDraft)===snapshot)requestDraft=requestClone(response.settings);
    requestMessage(requestDirty()?'Saved. You have additional unsaved changes.':'Request settings saved.');
  }catch(e){requestMessage(e.message,true)}
  finally{requestSaving=false;updateRequestSaveState()}
}
function setRequestPanel(panel){requestPanel=panel;renderRequests();if(panel==='queue')loadAdminQueue()}
function requestNumber(path,label,min,max,step=1,hint=''){
  return '<label class="request-field"><span>'+label+'</span><input type="number" required min="'+min+'" max="'+max+'" step="'+step+'" value="'+esc(requestValue(path))+'" oninput="setRequestField(\''+path+'\',this.value.length?Number(this.value):NaN)">'+(hint?'<small>'+hint+'</small>':'')+'</label>';
}
function requestCheck(path,label,hint=''){
  return '<label class="request-check"><input type="checkbox" '+(requestValue(path)?'checked':'')+' onchange="setRequestField(\''+path+'\',this.checked)"><span>'+label+(hint?'<small>'+hint+'</small>':'')+'</span></label>';
}
function requestSelect(path,label,options,hint='',rerender=false){
  return '<label class="request-field"><span>'+label+'</span><select onchange="setRequestField(\''+path+'\',this.value);'+(rerender?'renderRequests();':'')+'">'+options.map(o=>'<option value="'+esc(o.id)+'" '+(requestValue(path)===o.id?'selected':'')+'>'+esc(o.label)+'</option>').join('')+'</select>'+(hint?'<small>'+hint+'</small>':'')+'</label>';
}
function renderRequests(){
  document.querySelectorAll('#requestPanelNav button').forEach(button=>button.classList.toggle('active',button.dataset.panel===requestPanel));
  if(!requestDraft)return;
  const titles={queue:'Request queue',general:'Request defaults',trackers:'Trackers',movie:'Movie profile',tv:'TV profile'};
  $('requestsPanelTitle').textContent=titles[requestPanel];
  $('requestsSettingsContent').innerHTML=requestPanel==='queue'?renderRequestQueue():requestPanel==='general'?renderRequestGeneral():requestPanel==='trackers'?renderRequestTrackers():renderRequestProfile(requestPanel);
  updateRequestSaveState();
}
function renderRequestGeneral(){
  return '<div class="request-section"><h3>Permissions and approval</h3><p class="small">Defaults for user requests. A limit of 0 means unlimited; a TV limit counts series.</p><div class="request-grid">'+requestCheck('allow_movies','Allow movie requests')+requestCheck('allow_tv','Allow TV requests')+requestCheck('auto_approve','Automatically approve requests')+requestCheck('search_on_approval','Search when a request is approved')+requestNumber('movie_limit','Movie limit per user',0,10000)+requestNumber('tv_limit','Series limit per user',0,10000)+requestNumber('limit_days','Limit window (days)',1,365)+'</div></div>'+
    '<div class="request-section"><h3>Search and download policy</h3><div class="request-grid">'+requestNumber('search_interval_seconds','Delay between tracker searches (seconds)',1,3600)+requestNumber('retry_interval_hours','Retry missing releases after (hours)',1,720)+requestNumber('release_delay_minutes','Wait for a preferred release (minutes)',0,10080,1,'0 means no delay after approval.')+requestNumber('min_seeders','Minimum seeders',0,100000)+requestNumber('max_concurrent_downloads','Maximum simultaneous request downloads',1,100)+requestNumber('stalled_minutes','Consider stalled after (minutes)',0,10080,1,'0 disables automatic stalled-download recovery.')+requestCheck('retry_failed_downloads','Try another release after a failed download')+'</div></div>';
}
function requestDriveRoot(drive,kind,target){
  return kind==='movie'?(target==='main'?drive.MovieRoot:drive.MovieRemoteRoot):(target==='main'?drive.TVRoot:drive.TVRemoteRoot);
}
function renderRequestProfile(kind){
  const profile=requestDraft[kind],isTV=kind==='tv',drives=requestData.drives||[];
  const eligible=drives.filter(d=>requestDriveRoot(d,kind,profile.destination));
  const driveOptions=[{id:'',label:'Automatic — most free space'},...eligible.map(d=>({id:d.ID,label:d.ID}))];
  if(profile.new_title_drive&&!eligible.some(d=>d.ID===profile.new_title_drive))driveOptions.push({id:profile.new_title_drive,label:profile.new_title_drive+' — unavailable for this destination'});
  const roots=eligible.filter(d=>!profile.new_title_drive||d.ID===profile.new_title_drive).map(d=>'<div><span>'+esc(d.ID)+'</span><code>'+esc(requestDriveRoot(d,kind,profile.destination))+'</code></div>').join('');
  let html='<div class="request-section"><h3>Library destination</h3><div class="request-grid">'+requestSelect(kind+'.destination','Add requested '+(isTV?'shows':'movies')+' to',[{id:'remote',label:'Remote library'},{id:'main',label:'Main library'}],'Only copies in the selected library satisfy a request.',true)+requestSelect(kind+'.new_title_drive','Disk for new titles',driveOptions,'Existing titles always stay on their current disk.',true)+'</div><p class="small">CineRoute checks main and remote folders before choosing a disk. All seasons and episodes of a show stay together.</p><div class="request-roots">'+(roots||'<p class="small">No matching library roots are configured for this selection.</p>')+'</div></div>';
  html+='<div class="request-section"><h3>Allowed qualities</h3><p class="small">Most preferred first. Qualities outside this list are rejected. Keyword scores rank releases within the same quality; tracker priority breaks ties.</p><div class="request-quality-list">'+profile.qualities.map((id,index)=>{
    const quality=requestData.qualities.find(q=>q.id===id);
    return '<div class="request-quality"><span class="small">'+(index+1)+'</span><strong>'+esc(quality?quality.label:id)+'</strong><button type="button" aria-label="Move '+esc(quality?quality.label:id)+' up" '+(index===0?'disabled':'')+' onclick="moveRequestQuality(\''+kind+'\','+index+',-1)">↑</button><button type="button" aria-label="Move '+esc(quality?quality.label:id)+' down" '+(index===profile.qualities.length-1?'disabled':'')+' onclick="moveRequestQuality(\''+kind+'\','+index+',1)">↓</button><button type="button" '+(profile.qualities.length===1?'disabled':'')+' onclick="removeRequestQuality(\''+kind+'\','+index+')">Remove</button></div>';
  }).join('')+'</div>';
  const available=requestData.qualities.filter(q=>!profile.qualities.includes(q.id));
  html+='<div class="row" style="margin-top:10px"><select id="requestAddQuality">'+available.map(q=>'<option value="'+esc(q.id)+'">'+esc(q.label)+'</option>').join('')+'</select><button type="button" '+(!available.length?'disabled':'')+' onclick="addRequestQuality(\''+kind+'\')">Add quality</button></div><div class="request-grid" style="margin-top:16px">'+requestCheck(kind+'.upgrade_allowed','Upgrade existing requested copies')+requestSelect(kind+'.upgrade_until','Stop upgrading at',profile.qualities.map(id=>requestData.qualities.find(q=>q.id===id)))+requestNumber(kind+'.minimum_score','Minimum keyword score',-100000,100000)+requestNumber(kind+'.upgrade_until_score','Stop upgrading at keyword score',-100000,100000,1,'Quality and score targets must both be met.')+'</div></div>';
  html+='<div class="request-section"><h3>File size and playback</h3><p class="small">Sizes are in GiB. A maximum of 0 means unlimited.'+(isTV?' Episode limits apply to individual episodes; packs have a separate total limit.':' Limits apply to the entire movie release.')+'</p><div class="request-grid">'+requestNumber(kind+'.min_size_gib',isTV?'Minimum episode size':'Minimum movie size',0,100000,0.1)+requestNumber(kind+'.max_size_gib',isTV?'Maximum episode size':'Maximum movie size',0,100000,0.1)+(isTV?requestNumber(kind+'.max_pack_size_gib','Maximum season / series pack size',0,100000,0.1):'')+'</div><div class="request-codecs"><span class="small">Allowed video codecs</span>'+requestData.codecs.map(codec=>'<label><input type="checkbox" '+(profile.codecs.includes(codec.id)?'checked':'')+' onchange="toggleRequestCodec(\''+kind+'\',\''+codec.id+'\',this.checked)"> '+esc(codec.label)+'</label>').join('')+'</div><div class="request-grid"><label class="request-field"><span>Audio languages</span><input type="text" value="'+esc(profile.audio_languages.join(', '))+'" placeholder="Any language" oninput="setRequestField(\''+kind+'.audio_languages\',this.value.split(\',\').map(s=>s.trim()).filter(Boolean))"><small>Comma-separated language codes: en, es, sv, or original. Leave blank for any language.</small></label>'+requestCheck(kind+'.allow_unknown_language','Accept releases with unknown audio language','Applies when audio languages are specified.')+'</div></div>';
  if(isTV){
    html+='<div class="request-section"><h3>TV monitoring</h3><div class="request-grid">'+requestSelect('tv_monitor','Default episode coverage',[{id:'requested_seasons',label:'Selected seasons only'},{id:'all_existing',label:'All existing episodes'},{id:'future',label:'Future episodes only'},{id:'all',label:'Existing and future episodes'}])+requestCheck('include_specials','Include specials (season 0)')+requestCheck('prefer_season_packs','Prefer season packs over individual episodes')+'</div></div>';
  }else{
    html+='<div class="request-section"><h3>Movie availability</h3>'+requestSelect('movie_availability','Start looking for a movie when',[{id:'released',label:'Released for home viewing'},{id:'in_cinemas',label:'In cinemas'},{id:'announced',label:'Announced'}])+'</div>';
  }
  html+='<div class="request-section"><h3>Keyword rules</h3><p class="small">Text matching ignores case. Every required rule must match; any rejected rule blocks a release. Preferred rules add their scores. Regular expressions are case-sensitive unless you add (?i).</p><div class="request-keywords">'+profile.keywords.map((rule,index)=>'<div class="request-keyword"><label><span class="small">Action</span><select onchange="setRequestKeyword(\''+kind+'\','+index+',\'mode\',this.value)">'+[{id:'preferred',label:'Prefer / score'},{id:'required',label:'Require'},{id:'excluded',label:'Reject'}].map(o=>'<option value="'+o.id+'" '+(rule.mode===o.id?'selected':'')+'>'+o.label+'</option>').join('')+'</select></label><label><span class="small">Match</span><select onchange="setRequestKeyword(\''+kind+'\','+index+',\'match\',this.value)"><option value="contains" '+(rule.match==='contains'?'selected':'')+'>Text contains</option><option value="regex" '+(rule.match==='regex'?'selected':'')+'>Regular expression</option></select></label><label class="request-pattern"><span class="small">Keyword or pattern</span><input type="text" required maxlength="500" value="'+esc(rule.pattern)+'" placeholder="e.g. DUAL, WEB-DL, release group" oninput="setRequestKeyword(\''+kind+'\','+index+',\'pattern\',this.value)"></label><label><span class="small">Score</span><input type="number" required min="-10000" max="10000" value="'+rule.score+'" '+(rule.mode!=='preferred'?'disabled':'')+' oninput="setRequestKeyword(\''+kind+'\','+index+',\'score\',this.value.length?Number(this.value):NaN)"></label><button type="button" onclick="removeRequestKeyword(\''+kind+'\','+index+')">Remove</button></div>').join('')+'</div><button type="button" style="margin-top:10px" onclick="addRequestKeyword(\''+kind+'\')">Add keyword rule</button></div>';
  return html;
}
function moveRequestQuality(kind,index,direction){
  const items=requestDraft[kind].qualities,next=index+direction;
  if(next<0||next>=items.length)return;
  [items[index],items[next]]=[items[next],items[index]];renderRequests();
}
function removeRequestQuality(kind,index){
  const profile=requestDraft[kind];if(profile.qualities.length<=1)return;
  profile.qualities.splice(index,1);
  if(!profile.qualities.includes(profile.upgrade_until))profile.upgrade_until=profile.qualities[0];
  renderRequests();
}
function addRequestQuality(kind){
  const value=$('requestAddQuality').value;
  if(value&&!requestDraft[kind].qualities.includes(value)){requestDraft[kind].qualities.push(value);renderRequests()}
}
function toggleRequestCodec(kind,id,checked){
  const values=requestDraft[kind].codecs;
  if(checked&&!values.includes(id))values.push(id);
  if(!checked)requestDraft[kind].codecs=values.filter(value=>value!==id);
  updateRequestSaveState();
}
function addRequestKeyword(kind){requestDraft[kind].keywords.push({pattern:'',match:'contains',mode:'preferred',score:10});renderRequests()}
function removeRequestKeyword(kind,index){requestDraft[kind].keywords.splice(index,1);renderRequests()}
function setRequestKeyword(kind,index,field,value){
  const rule=requestDraft[kind].keywords[index];rule[field]=value;
  if(field==='mode'){if(value!=='preferred')rule.score=0;renderRequests()}else updateRequestSaveState();
}
async function loadRequestIndexers(){
  if(currentAccount?.role!=="admin")return;const owner=currentAccount.name;
  $('requestsTrackerRefresh')?.setAttribute('disabled','disabled');
  try{const response=await api('/api/requests/indexers');if(currentAccount?.role!=="admin"||currentAccount.name!==owner)return;requestIndexers=response.indexers||[];requestIndexersLoaded=true;requestIndexerError=''}
  catch(e){requestIndexerError=e.message}
  if(requestPanel==='trackers')renderRequests();
}
function renderRequestTrackers(){
  const rows=new Map(requestIndexers.map(indexer=>[indexer.id,indexer]));
  requestDraft.trackers.forEach(tracker=>{if(!rows.has(tracker.id))rows.set(tracker.id,{id:tracker.id,name:tracker.name||'Tracker #'+tracker.id,missing:true})});
  const trackers=[...rows.values()].sort((a,b)=>a.name.localeCompare(b.name));
  return '<div class="request-section"><div class="copy-toolbar"><div><h3>Choose Prowlarr trackers</h3><p class="small">Only selected, enabled torrent trackers will be used. Lower priority numbers win ties. No trackers are selected automatically.</p></div><button type="button" id="requestsTrackerRefresh" '+(!requestData.prowlarr_configured?'disabled':'')+' onclick="loadRequestIndexers()">Refresh trackers</button></div>'+(!requestData.prowlarr_configured?'<p class="small">Configure the existing Prowlarr URL and API key to load your trackers.</p>':'')+(requestIndexerError?'<div class="errbox">'+esc(requestIndexerError)+' Saved selections are retained.</div>':'')+'<div class="request-trackers">'+trackers.map(indexer=>{
    const selected=requestDraft.trackers.find(t=>t.id===indexer.id),eligible=indexer.enable&&indexer.protocol==='torrent';
    const note=indexer.missing?(requestIndexersLoaded?'Not returned by Prowlarr':'Saved selection; refresh to check availability'):!indexer.enable?'Disabled in Prowlarr':indexer.protocol!=='torrent'?'Not a torrent tracker':'Torrent tracker';
    return '<div class="request-tracker"><label class="request-check"><input type="checkbox" '+(selected?'checked':'')+' '+(!eligible&&!selected?'disabled':'')+' onchange="toggleRequestTracker('+indexer.id+',this.checked)"><span>'+esc(indexer.name)+'<small>'+esc(note)+'</small></span></label><label class="request-field"><span>Priority</span><input type="number" required min="1" max="100" '+(!selected?'disabled':'')+' value="'+(selected?selected.priority:25)+'" oninput="setRequestTrackerPriority('+indexer.id+',this.value.length?Number(this.value):NaN)"></label></div>';
  }).join('')+(trackers.length?'':'<p class="small">No trackers loaded. Refresh to choose from your Prowlarr configuration.</p>')+'</div><p class="small">'+requestDraft.trackers.length+' tracker'+(requestDraft.trackers.length===1?'':'s')+' selected.</p></div>';
}
function toggleRequestTracker(id,checked){
  if(checked){const indexer=requestIndexers.find(i=>i.id===id);if(!indexer||!indexer.enable||indexer.protocol!=='torrent')return;if(!requestDraft.trackers.some(t=>t.id===id))requestDraft.trackers.push({id,name:indexer.name,priority:25})}
  else requestDraft.trackers=requestDraft.trackers.filter(t=>t.id!==id);
  renderRequests();
}
function setRequestTrackerPriority(id,value){const tracker=requestDraft.trackers.find(t=>t.id===id);if(tracker){tracker.priority=value;updateRequestSaveState()}}

let adminQueue=[],adminQueueLoading=false;
async function loadSession(){
  try{
    const session=await api('/api/session');currentAccount=session.account;csrf=session.csrf_token||'';portalMode=currentAccount.role!=='admin';showApp();
    if(currentAccount.role==='admin'){showView('route',false);loadStatus();loadHistory();refresh()}else{loadMyRequests();$('catalogQuery').focus()}
  }catch(e){showLogin()}
}
function openUserPortal(){if(!currentAccount)return;portalMode=true;showApp();loadMyRequests();$('catalogQuery').focus()}
function openAdministration(){if(currentAccount?.role!=='admin')return;portalMode=false;showApp();loadStatus();refresh()}
async function signOut(){
  try{await postJSON('/api/logout',{})}catch(e){};
  currentAccount=null;portalMode=false;resetPortal();requestData=null;requestDraft=null;requestSavedJSON='';showLogin();
}
async function loadAdminQueue(){
  if(currentAccount?.role!=='admin'||adminQueueLoading)return;adminQueueLoading=true;
  try{const response=await api('/api/requests/queue');if(currentAccount?.role!=='admin')return;adminQueue=response.requests||[];if(requestPanel==='queue')$('requestsSettingsContent').innerHTML=renderRequestQueue()}
  catch(e){requestMessage(e.message,true)}finally{adminQueueLoading=false}
}
function renderRequestQueue(){return '<div class="copy-toolbar"><p class="small">'+adminQueue.length+' requests · '+adminQueue.filter(row=>row.request.status==='pending').length+' awaiting approval</p><button type="button" onclick="loadAdminQueue()">Refresh queue</button></div>'+adminQueue.map(row=>{const job=row.request;let actions=job.status==='pending'?['approve','decline']:!['cancelled','declined'].includes(job.status)?['retry','cancel']:[];return '<article class="admin-request"><div class="copy-toolbar"><div><strong>'+esc(mediaTitle(job.media))+'</strong><div class="small">'+esc(row.owner)+' · '+esc(row.destination)+' · '+portalStatus(job.status)+'</div></div><div class="row">'+actions.map(action=>'<button type="button" data-id="'+esc(job.id)+'" data-action="'+action+'" onclick="adminRequestAction(this.dataset.id,this.dataset.action,this)">'+action[0].toUpperCase()+action.slice(1)+'</button>').join('')+'</div></div><p class="small">'+esc(job.message)+'</p>'+(row.diagnostic?'<div class="request-diagnostic">'+esc(row.diagnostic)+'</div>':'')+'</article>'}).join('')+(adminQueue.length?'':'<p class="small">No requests yet. Use Search & request to add one.</p>')}
async function adminRequestAction(id,action,button){button.disabled=true;try{await postJSON('/api/requests/queue/'+encodeURIComponent(id)+'/'+action,{});await loadAdminQueue()}catch(e){requestMessage(e.message,true);button.disabled=false}}
