// Role-aware discovery and personal requests. Server permissions remain authoritative.
let currentAccount=null,portalMode=false,catalogItems=[],catalogPage=1,catalogPages=1,catalogFilter='all',catalogJobs=[],catalogDetail=null,catalogSearchToken=0,catalogDetailToken=0,catalogJobsToken=0,catalogController=null;
const mediaTitle=media=>media.title||media.name||'';
const mediaYear=media=>(media.release_date||media.first_air_date||'').slice(0,4);
const posterURL=path=>/^\/[A-Za-z0-9_.-]+\.(jpg|png|webp)$/i.test(path||'')?'https://image.tmdb.org/t/p/w342'+path:'';
const portalStatus=status=>({pending:'Awaiting approval',queued:'Queued',searching:'Finding a release',waiting:'Waiting for a release',downloading:'Downloading',complete:'Ready',monitoring:'Monitoring',error:'Needs attention',declined:'Declined',cancelled:'Canceled'}[status]||status);
const portalStatusClass=status=>status==='complete'?'ok':status==='error'||status==='declined'?'err':status==='downloading'||status==='pending'?'warn':'';
function resetPortal(){
  catalogSearchToken++;catalogDetailToken++;catalogJobsToken++;catalogController?.abort();catalogItems=[];catalogJobs=[];catalogDetail=null;catalogPage=1;
  $('catalogQuery').value='';$('catalogResults').innerHTML='';$('catalogDetail').innerHTML='';$('catalogDetail').classList.add('hidden');$('catalogMessage').textContent='';$('myRequestList').innerHTML='';
}
function setCatalogFilter(value){catalogFilter=value;document.querySelectorAll('#catalogFilters button').forEach(button=>button.classList.toggle('active',button.dataset.type===value));if($('catalogQuery').value.trim().length>=2)searchCatalog()}
async function searchCatalog(more=false){
  const query=$('catalogQuery').value.trim();if(query.length<2){$('catalogMessage').textContent='Enter a movie or show title.';return}
  const token=++catalogSearchToken;catalogController?.abort();catalogController=new AbortController();const generation=currentAccount?.name;
  if(!more){catalogItems=[];catalogPage=1;catalogDetail=null;$('catalogDetail').classList.add('hidden');$('catalogResults').innerHTML=''}
  $('catalogMessage').textContent='Searching…';$('catalogSearchButton').disabled=true;$('catalogHero').classList.add('has-search');
  try{
    const response=await api('/api/catalog/search?q='+encodeURIComponent(query)+'&type='+catalogFilter+'&page='+(more?catalogPage+1:1),{signal:catalogController.signal});
    if(token!==catalogSearchToken||generation!==currentAccount?.name)return;
    catalogItems=more?[...catalogItems,...response.results]:response.results;catalogPage=response.page;catalogPages=response.total_pages;
    renderCatalog();$('catalogMessage').textContent=catalogItems.length?'':'No matching titles. Try another name or year.';
  }catch(e){if(e.name!=='AbortError'&&token===catalogSearchToken)$('catalogMessage').textContent=e.message}
  finally{if(token===catalogSearchToken)$('catalogSearchButton').disabled=false}
}
function renderCatalog(){
  $('catalogResults').innerHTML=catalogItems.map(media=>{
    const image=posterURL(media.poster_path),requested=catalogJobs.find(job=>job.media.id===media.id&&job.media.media_type===media.media_type&&!['cancelled','declined'].includes(job.status));
    return '<button class="catalog-card" type="button" data-id="'+media.id+'" data-type="'+esc(media.media_type)+'" onclick="openCatalogTitle(this.dataset.type,Number(this.dataset.id))"><span class="catalog-poster">'+(image?'<img src="'+esc(image)+'" alt="" loading="lazy">':'<span class="catalog-no-poster">'+esc(mediaTitle(media))+'</span>')+'</span><span class="catalog-card-title">'+esc(mediaTitle(media))+'</span><span class="catalog-card-meta">'+esc(mediaYear(media))+' · '+(media.media_type==='tv'?'TV':'Movie')+'</span>'+(requested?'<span class="badge '+portalStatusClass(requested.status)+'">'+portalStatus(requested.status)+'</span>':'')+'</button>';
  }).join('');
  $('catalogMore').classList.toggle('hidden',!catalogItems.length||catalogPage>=catalogPages);
}
async function openCatalogTitle(kind,id){
  const token=++catalogDetailToken,generation=currentAccount?.name;$('catalogMessage').textContent='Loading title…';
  try{
    const detail=await api('/api/catalog/'+kind+'/'+id);
    if(token!==catalogDetailToken||generation!==currentAccount?.name)return;
    catalogDetail=detail;renderCatalogDetail();$('catalogMessage').textContent='';$('catalogDetail').scrollIntoView({behavior:'smooth',block:'start'});
  }catch(e){if(token===catalogDetailToken)$('catalogMessage').textContent=e.message}
}
function closeCatalogTitle(){catalogDetailToken++;catalogDetail=null;$('catalogDetail').classList.add('hidden')}
function renderCatalogDetail(){
  if(!catalogDetail)return;const detail=catalogDetail,media=detail.media,isTV=media.media_type==='tv',image=posterURL(media.poster_path),existing=catalogJobs.find(job=>job.media.id===media.id&&job.media.media_type===media.media_type&&!['cancelled','declined'].includes(job.status));
  const seasons=(detail.seasons||[]).filter(season=>season.episode_count>0).map(season=>'<label class="catalog-season"><input type="checkbox" value="'+season.season_number+'" '+(!season.air_date||season.air_date<=new Date().toISOString().slice(0,10)?'checked':'')+'> <span>'+esc(season.name||'Season '+season.season_number)+'<small>'+season.episode_count+' episodes'+(season.air_date?' · '+esc(season.air_date.slice(0,4)):'')+'</small></span></label>').join('');
  $('catalogDetail').classList.remove('hidden');$('catalogDetail').innerHTML='<div class="catalog-detail-head">'+(image?'<img class="catalog-detail-poster" src="'+esc(image)+'" alt="">':'')+'<div><div class="copy-toolbar"><h2>'+esc(mediaTitle(media))+(mediaYear(media)?' ('+esc(mediaYear(media))+')':'')+'</h2><button type="button" onclick="closeCatalogTitle()">Close</button></div><span class="badge">'+(isTV?'TV series':'Movie')+'</span><p>'+esc(media.overview||'No description available.')+'</p></div></div>'+(isTV?'<h3>Choose seasons</h3><div id="catalogSeasons" class="catalog-seasons">'+(seasons||'<p class="small">No seasons announced yet.</p>')+'</div><label class="request-check" style="margin-top:14px"><input id="catalogFollowFuture" type="checkbox" '+(['all','future'].includes(detail.monitor)?'checked':'')+'> <span>Follow future seasons</span></label>'+(detail.monitor==='future'?'<p class="small">The library policy requests only episodes airing from today onward.</p>':''):'')+'<div id="catalogRequestMessage" role="status" aria-live="polite"></div><div class="actions"><button id="catalogRequestButton" type="button" class="primary" '+(!isTV&&existing?'disabled':'')+' onclick="submitCatalogRequest()">'+(existing?(isTV?'Add selected seasons':portalStatus(existing.status)):detail.auto_approve?'Add to library':'Request approval')+'</button><span class="small">'+(existing?esc(existing.message):detail.auto_approve?'We’ll find a release that matches the library settings.':'Your administrator will review this request.')+'</span></div>';
}
async function submitCatalogRequest(){
  if(!catalogDetail)return;const media=catalogDetail.media,generation=currentAccount?.name,button=$('catalogRequestButton');
  const body={media_type:media.media_type,tmdb_id:media.id};if(media.media_type==='tv'){body.seasons=[...document.querySelectorAll('#catalogSeasons input:checked')].map(input=>Number(input.value));body.follow_future=$('catalogFollowFuture').checked;if(!body.seasons.length&&!body.follow_future){$('catalogRequestMessage').textContent='Choose at least one season or follow future seasons.';return}}
  button.disabled=true;button.textContent='Saving request…';
  try{const response=await postJSON('/api/user/requests',body);if(generation!==currentAccount?.name)return;await loadMyRequests();if(catalogDetail?.media.id===media.id){renderCatalogDetail();$('catalogRequestMessage').textContent=response.request.message}}
  catch(e){if(generation===currentAccount?.name){$('catalogRequestMessage').textContent=e.message;button.disabled=false;button.textContent='Try again'}}
}
async function loadMyRequests(){
  if(!currentAccount)return;const token=++catalogJobsToken,generation=currentAccount.name;
  try{const response=await api('/api/user/requests');if(token!==catalogJobsToken||generation!==currentAccount?.name)return;catalogJobs=response.requests||[];renderMyRequests();if(catalogItems.length)renderCatalog()}
  catch(e){if(token===catalogJobsToken)$('myRequestMessage').textContent=e.message}
}
function renderMyRequests(){
  $('myRequestMessage').textContent='';$('myRequestList').innerHTML=catalogJobs.map(job=>{
    const image=posterURL(job.media.poster_path),progress=Math.max(0,Math.min(100,Math.round((job.progress||0)*100)));
    return '<article class="portal-request"><button type="button" class="portal-request-art" data-id="'+job.media.id+'" data-type="'+esc(job.media.media_type)+'" onclick="openCatalogTitle(this.dataset.type,Number(this.dataset.id))">'+(image?'<img src="'+esc(image)+'" alt="" loading="lazy">':'<span>▶</span>')+'</button><div class="portal-request-body"><strong>'+esc(mediaTitle(job.media))+'</strong><span class="small">'+esc(mediaYear(job.media))+(job.media.media_type==='tv'?' · '+(job.seasons||[]).map(n=>'S'+String(n).padStart(2,'0')).join(', ')+(job.follow_future?' · future seasons':''):'')+'</span><span class="small">'+esc(job.message)+'</span>'+(job.status==='downloading'?'<progress value="'+progress+'" max="100" aria-label="Download progress">'+progress+'%</progress>':'')+'</div><div class="portal-request-actions"><span class="badge '+portalStatusClass(job.status)+'">'+portalStatus(job.status)+'</span>'+(!['cancelled','declined','complete'].includes(job.status)?'<button type="button" data-id="'+esc(job.id)+'" onclick="cancelMyRequest(this.dataset.id,this)">Cancel request</button>':'')+'</div></article>';
  }).join('')||'<p class="small">Your requested movies and shows will appear here.</p>';
}
async function cancelMyRequest(id,button){if(!confirm('Cancel this request? Existing downloaded files and shared downloads are kept.'))return;button.disabled=true;try{await postJSON('/api/user/requests/'+encodeURIComponent(id)+'/cancel',{});await loadMyRequests()}catch(e){$('myRequestMessage').textContent=e.message;button.disabled=false}}
