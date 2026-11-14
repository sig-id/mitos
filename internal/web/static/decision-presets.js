(() => {
 'use strict';
 // Provider documentation and verification date are recorded in docs/agent-decisions.md.
 const presets=[
  {name:'TypeSafe Jev',protocol:'systemone',endpoint:'https://api.typesafe.ai/v1/systemone',model:'jev-latest',env:'TYPESAFE_API_KEY'},
  {name:'OpenAI',protocol:'openai_decisions',endpoint:'https://api.openai.com/v1/decisions',model:'gpt-6-luna',env:'OPENAI_API_KEY'},
  {name:'Gemini',protocol:'gemini_decisions',endpoint:'https://generativelanguage.googleapis.com/v1beta/decisions',model:'gemini-decision-1',env:'GEMINI_API_KEY'},
  {name:'OpenRouter Jev',protocol:'openrouter_decisions',endpoint:'https://openrouter.ai/api/alpha/decisions',model:'typesafe/jev-1.14',env:'OPENROUTER_API_KEY'},
  {name:'Cloudflare Clef (OpenRouter)',protocol:'openrouter_decisions',endpoint:'https://openrouter.ai/api/alpha/decisions',model:'cloudflare/clef-flash',env:'OPENROUTER_API_KEY'},
  {name:'Mercury Decide (OpenRouter)',protocol:'openrouter_decisions',endpoint:'https://openrouter.ai/api/alpha/decisions',model:'inception/mercury-decide',env:'OPENROUTER_API_KEY'}
 ];
 window.MitosDecisionPresets={render(parent,{protocol,endpoint,model,credential}){
  const box=document.createElement('div'),label=document.createElement('p'),buttons=document.createElement('div'),note=document.createElement('p');box.className='decision-presets';buttons.className='decision-preset-buttons';label.textContent='Prefill provider';note.className='muted';note.setAttribute('role','status');
  function refresh(){note.textContent='Fills in the endpoint, protocol, and model. You can edit them below.'}
  for(const preset of presets){const button=document.createElement('button');button.type='button';button.textContent=preset.name;button.onclick=()=>{const changed=endpoint.value!==preset.endpoint;protocol.value=preset.protocol;endpoint.value=preset.endpoint;model.value=preset.model;credential.preset(preset.env,changed);refresh()};buttons.append(button)}
  protocol.addEventListener('change',refresh);refresh();box.append(label,buttons,note);parent.insertBefore(box,protocol.parentElement);
 }};
})();
