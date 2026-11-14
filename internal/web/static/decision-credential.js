(() => {
 'use strict';
 window.MitosDecisionCredential={render(parent,config={}){
  let savedRef=config.api_key_ref||'';
  const field=(label,control)=>{const node=document.createElement('label');node.textContent=label;control.setAttribute('aria-label',label);node.append(control);parent.append(node);return node};
  const mode=document.createElement('select');mode.append(new Option('API key','key'),new Option('Environment variable','env'));mode.value=config.api_key_env?'env':'key';field('Authentication',mode);
  const key=document.createElement('input');key.type='password';key.autocomplete='new-password';key.maxLength=8192;key.spellcheck=false;field('API key',key);
  const env=document.createElement('input');env.value=config.api_key_env||'';env.placeholder='OPENAI_API_KEY';env.pattern='[A-Za-z_][A-Za-z0-9_]*';env.autocomplete='off';env.spellcheck=false;field('API key environment variable',env);
  const help=document.createElement('p'),remove=document.createElement('button');remove.type='button';remove.textContent='Remove saved key';parent.append(help,remove);
  function refresh(){
   key.parentElement.hidden=mode.value!=='key';key.disabled=mode.value!=='key';env.parentElement.hidden=mode.value!=='env';env.disabled=mode.value!=='env';remove.hidden=mode.value!=='key'||!savedRef;
   key.placeholder=savedRef?'Saved key (leave blank to keep it)':'Paste your API key';
   help.textContent=mode.value==='env'?'Enter the environment variable name, such as OPENAI_API_KEY. Set its value before starting Mitos; restart Mitos after changing it.':savedRef?'An API key is saved. Paste a new key to replace it.':'Paste the key here. It is stored privately on this Mitos server when you save. No restart needed. Leave blank if your provider requires no key.';
  }
  mode.onchange=refresh;remove.onclick=()=>{savedRef='';key.value='';refresh()};refresh();
  return {
   value(){return mode.value==='env'?{api_key_env:env.value.trim()}:key.value.trim()?{api_key:key.value.trim()}:savedRef?{api_key_ref:savedRef}:{}},
   preset(name,changed){if(changed){savedRef='';key.value='';env.value=name}env.placeholder=name;refresh()},
   saved(config){savedRef=config?.api_key_ref||'';key.value='';refresh()}
  };
 }};
})();
