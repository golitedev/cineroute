// The internet listener contains only request-portal behavior.
const $=id=>document.getElementById(id);
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let csrf='';
async function api(path,opts={}){
 opts={...opts,headers:{...opts.headers}};
 if(opts.method&&!['GET','HEAD','OPTIONS'].includes(opts.method.toUpperCase())&&csrf)opts.headers['X-CineRoute-CSRF']=csrf;
 const response=await fetch(path,opts),body=await response.json().catch(()=>({}));
 if(response.status===401)showLogin();
 if(!response.ok)throw new Error(body.error||'Request failed.');
 return body;
}
function postJSON(path,body){return api(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})}
function showApp(){
 if(currentAccount?.role!=='user'){showLogin();return}
 $('loginCard').classList.add('hidden');$('userShell').classList.remove('hidden');$('accountBar').classList.remove('hidden');$('accountName').textContent=currentAccount.name;
}
function showLogin(){
 currentAccount=null;csrf='';resetPortal();$('userShell').classList.add('hidden');$('accountBar').classList.add('hidden');$('loginCard').classList.remove('hidden');$('loginPass').value='';
}
async function loadSession(){
 try{const session=await api('/api/session');if(session.account?.role!=='user')throw new Error('Use a user account.');currentAccount=session.account;csrf=session.csrf_token||'';showApp();await loadMyRequests();$('catalogQuery').focus()}
 catch(e){showLogin()}
}
async function login(){
 $('loginErr').classList.add('hidden');$('loginBtn').disabled=true;
 try{await postJSON('/api/login',{username:$('loginUser').value,password:$('loginPass').value});$('loginPass').value='';await loadSession()}
 catch(e){$('loginErr').textContent=e.message;$('loginErr').classList.remove('hidden')}
 finally{$('loginBtn').disabled=false}
}
async function signOut(){try{await postJSON('/api/logout',{})}catch(e){}showLogin()}
function togglePass(){const input=$('loginPass');input.type=input.type==='password'?'text':'password';$('loginToggle').setAttribute('aria-label',input.type==='password'?'Show password':'Hide password')}
for(const id of ['loginUser','loginPass'])$(id).addEventListener('keydown',event=>{if(event.key==='Enter')login()});
loadSession();
setInterval(()=>{if(currentAccount)loadMyRequests()},15000);
