package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPublicPortalLoginSearchAndCSRFSubmission(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	catalog, err := os.ReadFile("templates/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	portal, err := os.ReadFile("templates/portal.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(catalog) + "\n" + string(portal) + "\n" + publicFrontendChecks
	// Define the fake browser and HTTP service before executing production scripts.
	path := filepath.Join(t.TempDir(), "portal-test.cjs")
	if err := os.WriteFile(path, []byte(publicFrontendEnvironment+script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("public frontend: %v\n%s", err, out)
	}
}

const publicFrontendEnvironment = `
const elements=new Map();
const document={getElementById(id){if(!elements.has(id)){const classes=new Set(['hidden']);elements.set(id,{value:'',innerHTML:'',textContent:'',disabled:false,type:'password',classList:{add:x=>classes.add(x),remove:x=>classes.delete(x),contains:x=>classes.has(x),toggle(x,value){value?classes.add(x):classes.delete(x)}},addEventListener(){},setAttribute(){},focus(){},scrollIntoView(){}})}return elements.get(id)},querySelectorAll(){return []}};
const setInterval=()=>0;
const assert=(condition,message)=>{if(!condition)throw new Error(message)};
let signedIn=false,created=false,requestedSpanish=false;
const media={id:101,title:'Example Movie',media_type:'movie',release_date:'2024-01-01',poster_path:'/movie.jpg'};
const fetch=async(path,options={})=>{
 let body={},status=200;
 if(path==='/api/login'){assert(options.headers['Content-Type']==='application/json','login content type');signedIn=true}
 else if(!signedIn){status=401;body={error:'unauthorized'}}
 else if(path==='/api/session')body={account:{name:'alice',role:'user'},csrf_token:'test-csrf-token'};
 else if(path.startsWith('/api/catalog/search'))body={results:[media],page:1,total_pages:1,similar_matches:true};
 else if(path==='/api/catalog/movie/101')body={media,seasons:[],auto_approve:true};
 else if(path==='/api/user/requests'&&options.method==='POST'){
  assert(options.headers['X-CineRoute-CSRF']==='test-csrf-token','request missing CSRF');
  const submitted=JSON.parse(options.body);assert(submitted.tmdb_id===101&&!('destination' in submitted),'unexpected request payload');assert(typeof submitted.prefer_spanish_audio==='boolean'&&!('trackers' in submitted)&&!('priority' in submitted),'Spanish preference must be a constrained boolean');requestedSpanish=submitted.prefer_spanish_audio;created=true;body={request:{message:'Queued',prefer_spanish_audio:requestedSpanish}};
 }
 else if(path==='/api/user/requests')body={requests:created?[{id:'request-one',media,status:'queued',message:'Queued',prefer_spanish_audio:requestedSpanish}]:[]};
 else if(path==='/api/logout'){assert(options.headers['X-CineRoute-CSRF']==='test-csrf-token','logout missing CSRF');signedIn=false}
 else throw new Error('unexpected/admin API: '+path);
 return {ok:status===200,status,json:async()=>body};
};
`
const publicFrontendChecks = `
(async()=>{
 await new Promise(resolve=>setImmediate(resolve));
 assert(!currentAccount,'anonymous user authenticated');
 $('loginUser').value='alice';$('loginPass').value='secret';await login();
 assert(currentAccount?.role==='user'&&!$('userShell').classList.contains('hidden'),'user login did not open portal');
 $('catalogQuery').value='Example';await searchCatalog();
 assert($('catalogResults').innerHTML.includes('Example Movie'),'search not rendered');
 assert($('catalogMessage').textContent.includes('similar titles'),'fallback results are not labelled');
 await openCatalogTitle('movie',101);assert($('catalogDetail').innerHTML.includes('catalogSpanishAudio'),'Spanish preference checkbox missing');setCatalogSpanishAudio(true);await submitCatalogRequest();
 assert(created&&requestedSpanish&&$('myRequestList').innerHTML.includes('Spanish audio preferred'),'Spanish request flow did not finish');await openCatalogTitle('movie',101);assert(catalogSpanishAudio&&$('catalogDetail').innerHTML.includes('checked'),'saved Spanish preference not restored');setCatalogSpanishAudio(false);assert(!$('catalogRequestButton').disabled&&$('catalogRequestButton').textContent==='Update request','existing movie preference cannot be updated');await submitCatalogRequest();assert(!requestedSpanish&&!$('myRequestList').innerHTML.includes('Spanish audio preferred'),'unchecking preference failed');
 await signOut();
 assert(!currentAccount&&csrf===''&&!catalogSpanishAudio&&$('myRequestList').innerHTML==='','logout retained session/request data');
})().catch(error=>{console.error(error);process.exitCode=1});
`
