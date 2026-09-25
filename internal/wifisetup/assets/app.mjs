import {RATE,encode,seal,sessionFromFragment} from './modem.mjs';
const $=id=>document.getElementById(id);
let session=null, context=null, source=null, generation=0;
function status(text){$('status').textContent=text;}
function stop(){generation++;if(source){source.onended=null;source.stop();source=null;}if(context){context.close();context=null;}$('stop').hidden=true;}
function clear(){stop();if(session)session.key.fill(0);session=null;$('pairing').classList.remove('paired');$('password').value='';$('code').value='';$('play').disabled=true;$('session').textContent='Waiting for a watch code';status('Scan the watch code to begin.');}
function importCode(value){
  stop(); const fragment=value.includes('#')?value.slice(value.indexOf('#')):value;
  try {const next=sessionFromFragment(fragment);if(session)session.key.fill(0);session=next;$('pairing').classList.add('paired');$('ssid').value=session.ssid;$('ssid').readOnly=!!session.ssid;$('play').disabled=false;$('session').textContent='Watch ready · session '+Array.from(session.sid.slice(0,2),x=>x.toString(16).padStart(2,'0')).join('');status('Ready. Keep the watch on Listening.');}catch(e){clear();status(e.message);}finally{$('code').value='';}
}
const initial=location.hash;history.replaceState(null,'',location.pathname);if(initial)importCode(initial);
window.addEventListener('hashchange',()=>{const hash=location.hash;history.replaceState(null,'',location.pathname);importCode(hash);});
$('import').onclick=()=>importCode($('code').value);
$('reveal').onclick=()=>{const hidden=$('password').type==='password';$('password').type=hidden?'text':'password';$('reveal').textContent=hidden?'Hide':'Show';};
$('open').onchange=()=>{$('password').disabled=$('open').checked;if($('open').checked)$('password').value='';};
$('clear').onclick=clear;$('stop').onclick=()=>{stop();status('Stopped. Tap Play to try again.');};
$('form').onsubmit=async e=>{
  e.preventDefault();if(!session)return;
  stop(); const turn=generation;const local=session;
  try {
    if(!crypto.subtle)throw Error('Open this page over trusted HTTPS or localhost.');
    context=new AudioContext();await context.resume();
    const packet=await seal($('ssid').value,$('open').checked?'':$('password').value,local);
    if(turn!==generation){packet.fill(0);return;}
    const pcm=encode(packet);packet.fill(0);const buffer=context.createBuffer(1,pcm.length,RATE);buffer.copyToChannel(pcm,0);pcm.fill(0);
    source=context.createBufferSource();source.buffer=buffer;source.connect(context.destination);$('stop').hidden=false;
    source.onended=()=>{if(turn!==generation)return;stop();status('Sound sent. Check the watch for connection status. Tap Play to retry.');};
    source.start();status('Playing… Keep the phone near the watch.');
  }catch(e){if(turn===generation){stop();status(e.message);}}
};
window.addEventListener('pagehide',clear);
if('serviceWorker' in navigator){navigator.serviceWorker.register('sw.js').then(()=>navigator.serviceWorker.ready).then(()=>{$('offline').textContent='Available offline on this phone.';}).catch(()=>{$('offline').textContent='Offline caching unavailable in this browser.';});}else $('offline').textContent='Keep this page open for offline use.';
