(()=>{'use strict';const $=(s,r=document)=>r.querySelector(s),$$=(s,r=document)=>[...r.querySelectorAll(s)],html=document.documentElement,reduce=matchMedia('(prefers-reduced-motion: reduce)'),scheme=matchMedia('(prefers-color-scheme: dark)');const searchBase=document.documentElement.dataset.rootAbs?'/':(document.documentElement.dataset.root?document.documentElement.dataset.root+'/':'');let toastTimer;const store={get:key=>{try{return localStorage.getItem(key);}catch{return null;}},set:(key,value)=>{try{localStorage.setItem(key,value);}catch{}}};function toast(text){const n=$('#toast');n.textContent=text;n.classList.add('visible');clearTimeout(toastTimer);toastTimer=setTimeout(()=>n.classList.remove('visible'),4600);}function applyTheme(mode){html.dataset.themePreference=mode;html.dataset.theme=mode==='system'?(scheme.matches?'dark':'light'):mode;store.set('artex-theme',mode);const label={system:'跟随系统',dark:'深色',light:'浅色'}[mode];const b=$('button[data-theme]');b.setAttribute('aria-label',`颜色主题：${label}，点击切换`);b.title=`当前：${label} · 点击切换深色 / 浅色 / 系统`;b.innerHTML=html.dataset.theme==='dark'?'<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/></svg>':'<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M20 14A8 8 0 0 1 10 4 8 8 0 1 0 20 14Z"/></svg>';}
applyTheme(html.dataset.themePreference||'system');$('button[data-theme]').addEventListener('click',()=>{const modes=['system','dark','light'];applyTheme(modes[(modes.indexOf(html.dataset.themePreference)+1)%modes.length]);});scheme.addEventListener('change',()=>{if(html.dataset.themePreference==='system')applyTheme('system');});
const menu=$('.mobile-menu'),nav=$('#nav-links');menu.addEventListener('click',()=>{const open=nav.classList.toggle('open');menu.setAttribute('aria-expanded',String(open));menu.setAttribute('aria-label',open?'关闭导航':'展开导航');});$$('a',nav).forEach(a=>a.addEventListener('click',()=>{nav.classList.remove('open');menu.setAttribute('aria-expanded','false');}));document.addEventListener('click',e=>{if(!e.target.closest('.nav')){nav.classList.remove('open');menu.setAttribute('aria-expanded','false');}});
const perspectives={joint:['看见边界。<br><em>深入本质。</em>','从网络表面，到二进制深处。<br>让不同领域的线索，汇成一条有依据的调查路径。'],network:['发现暴露。<br><em>厘清风险。</em>','从授权范围出发，连接目标、资产与接口。<br>让请求和响应成为依据，而不让工具输出代替判断。'],reverse:['深入指令。<br><em>读懂逻辑。</em>','从样本出发，走进函数与调用关系。<br>将内部逻辑转化为可追溯的理解。']};$$('[data-perspective]').forEach(b=>b.addEventListener('click',()=>{const data=perspectives[b.dataset.perspective];$$('[data-perspective]').forEach(a=>a.setAttribute('aria-pressed',String(a===b)));$('[data-hero-title]').innerHTML=data[0];$('[data-hero-copy]').innerHTML=data[1];}));
const motion=$('button[data-motion]');function motionState(){const paused=html.dataset.motion==='paused';motion.textContent=paused?'恢复动效':'暂停动效';motion.setAttribute('aria-pressed',String(paused));motion.title=reduce.matches?'系统已启用减少动态效果，此设置不会覆盖系统偏好':'';}motionState();motion.addEventListener('click',()=>{const paused=html.dataset.motion!=='paused';html.dataset.motion=paused?'paused':'running';store.set('artex-motion',paused?'paused':'running');motionState();});
const dialog=$('#search-dialog'),input=$('#site-search'),results=$('#search-results');let indexPromise=null,focusReturn=null,queryGeneration=0;function getIndex(){if(!indexPromise)indexPromise=window.ARTEX_SEARCH?Promise.resolve(window.ARTEX_SEARCH):Promise.reject(new Error('search index'));return indexPromise;}
async function search(){const generation=++queryGeneration;const value=input.value.trim().toLowerCase();try{const data=await getIndex();if(generation!==queryGeneration)return;const items=data.filter(p=>(p.title+p.label+p.description+p.keywords).toLowerCase().includes(value));results.replaceChildren();if(!items.length){const p=document.createElement('p');p.className='search-empty';p.textContent='没有匹配结果，试试「证据」「逆向」或「实测」。';results.append(p);}items.forEach(item=>{const a=document.createElement('a');a.href=searchBase+item.path;a.className='search-result';const div=document.createElement('div'),strong=document.createElement('strong'),p=document.createElement('p'),arrow=document.createElement('span');strong.textContent=item.label;p.textContent=item.description;div.append(strong,p);arrow.textContent='↗';arrow.setAttribute('aria-hidden','true');a.append(div,arrow);results.append(a);});}catch{results.replaceChildren();const p=document.createElement('p');p.className='search-empty';p.textContent='搜索索引暂不可用，请使用导航进入内容页面。';results.append(p);}}
function openSearch(){if(dialog.open)return;focusReturn=document.activeElement;dialog.showModal();document.body.style.overflow='hidden';input.value='';void search();input.focus();}function closeSearch(){dialog.close();}dialog.addEventListener('close',()=>{document.body.style.overflow='';queryGeneration++;if(focusReturn?.isConnected)focusReturn.focus();});$$('[data-search]').forEach(b=>b.addEventListener('click',openSearch));$('[data-close-search]').addEventListener('click',closeSearch);dialog.addEventListener('click',e=>{if(e.target===dialog){const r=dialog.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)closeSearch();}});input.addEventListener('input',()=>void search());dialog.addEventListener('keydown',e=>{const list=$$('.search-result',results);if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();const i=list.indexOf(document.activeElement);if(e.key==='ArrowDown')(list[(i+1)%list.length]||input).focus();else(i<=0?input:list[i-1]).focus();}if(e.key==='Enter'&&document.activeElement===input){e.preventDefault();list[0]?.click();}});document.addEventListener('keydown',e=>{if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='k'){e.preventDefault();dialog.open?closeSearch():openSearch();}if(e.key==='Escape'){nav.classList.remove('open');menu.setAttribute('aria-expanded','false');}});if(!/Mac|iPhone|iPad/.test(navigator.platform||'')){const k=$('.search-button kbd');if(k)k.textContent='Ctrl K';}
$$('[data-copy]').forEach(b=>b.addEventListener('click',async()=>{const value=document.getElementById(b.dataset.copy)?.textContent||'';try{await navigator.clipboard.writeText(value);toast('命令已复制。仅在独立官网目录执行。');}catch{toast('当前浏览器不允许复制，请手动选择命令文本。');}}));
$$('[data-filter]').forEach(b=>b.addEventListener('click',()=>{const f=b.dataset.filter;$$('[data-filter]').forEach(a=>{a.classList.toggle('active',a===b);a.setAttribute('aria-pressed',String(a===b));});$$('[data-capability]').forEach(card=>card.hidden=f!=='all'&&card.dataset.capability!==f);}));
const demo=$('[data-demo]');if(demo){let current='sample',passed=false;const data={sample:{title:'样本分析',id:'RE-001',copy:'静态理解客户端请求结构，保留函数和反编译证据，不将推断直接视为线上行为。',object:'client_demo.bin',source:'SAMPLE / a72f…91c0',state:'示例：静态分析完成',gate:'静态分析边界',gateCopy:'获准分析软件，不代表获准访问关联服务器。',action:'查看交接节点'},handoff:{title:'证据交接',id:'GATE-002',copy:'移交协议说明和来源引用，标明推断与待验证项；下游需要独立检查授权和验证条件。',object:'protocol_notes / v1',source:'RE-001 → EVIDENCE-02',state:'示例：待人工检查',gate:'交接检查点',gateCopy:'本次按钮只模拟确认，不代表执行真实审批或授权。',action:'模拟确认交接'},network:{title:'接口分析',id:'NET-003',copy:'协议说明是待验证线索，而不是已确认的线上行为。网络目标必须处于独立授权范围内。',object:'api.example.invalid',source:'GATE-002 / notes@v1',state:'示例：等待交接',gate:'独立网络授权',gateCopy:'此演示不会访问目标，也不会执行网络验证。',action:'查看阻塞原因'}};
function render(name){current=name;const d=data[name];$$('[data-node]',demo).forEach(b=>{const selected=b.dataset.node===name;b.classList.toggle('selected',selected);b.setAttribute('aria-pressed',String(selected));});const pairs={'detail-title':d.title,'detail-id':d.id,'detail-copy':d.copy,'detail-object':d.object,'detail-source':d.source,'detail-state':passed&&name==='handoff'?'示例：交接已确认':passed&&name==='network'?'示例：依赖已解除':d.state,'gate-title':d.gate,'gate-copy':d.gateCopy};for(const [key,value] of Object.entries(pairs))$(`[data-${key}]`,demo).textContent=value;$('[data-demo-action] span',demo).textContent=passed&&name==='handoff'?'查看下游任务':passed&&name==='network'?'查看执行边界':d.action;}
$$('[data-node]',demo).forEach(b=>b.addEventListener('click',()=>render(b.dataset.node)));$('[data-demo-action]',demo).addEventListener('click',()=>{if(current==='sample'){render('handoff');return;}if(current==='handoff'){if(passed){render('network');return;}passed=true;$('[data-network-state]',demo).textContent='→';$('[data-demo-status]',demo).textContent='DEMO HANDOFF ACCEPTED — NO TASK EXECUTED';render('handoff');toast('已模拟确认交接。下游仅解除演示依赖，未启动任务。');return;}toast(passed?'依赖已解除，但网络操作仍需独立授权。本页面不会执行操作。':'当前阻塞于 GATE-002：请先核对并模拟确认交接。');});const views={flow:['从内部逻辑，到网络验证。','THREE STAGES / ONE GOAL','sample'],evidence:['交接结论，也交接依据。','SOURCE / VERSION / LIMITATIONS','handoff'],scope:['共享目标，不默认共享权限。','STATIC SCOPE ≠ NETWORK SCOPE','network']};function view(name){$$('[data-demo-view]',demo).forEach(b=>{const active=b.dataset.demoView===name;b.classList.toggle('active',active);b.setAttribute('aria-pressed',String(active));});$('[data-canvas-title]',demo).textContent=views[name][0];$('[data-canvas-sub]',demo).textContent=views[name][1];render(views[name][2]);}$$('[data-demo-view]',demo).forEach(b=>b.addEventListener('click',()=>view(b.dataset.demoView)));$('[data-reset]',demo).addEventListener('click',()=>{passed=false;$('[data-network-state]',demo).textContent='⌁';$('[data-demo-status]',demo).textContent='LOCAL DEMO — NO BACKEND CONNECTED';view('flow');toast('本地演示已重置。');});view('flow');}

/* --------------------------------------------------------------------
   v3 视觉升级交互：页头滚动态 / 滚动显影 / 指针聚光 / 数字计数 /
   跑马灯 / Hero 视差。全部为渐进增强：出错只会回退到静态内容。
   -------------------------------------------------------------------- */
try{
const hoverable=matchMedia('(hover: hover)'),finePointer=matchMedia('(pointer: fine)');
const header=$('.site-header');
if(header){
const onScroll=()=>html.classList.toggle('scrolled',scrollY>8);
onScroll();
addEventListener('scroll',onScroll,{passive:true});
}
const revealList=['.section-head','.split-heading','.domain-card','.feature-card','.score-card','.provider-row','.stats','.statement-panel','.reverse-spec','.architecture-diagram','.editorial-grid>div','.callout','.demo','.faqs','.table-scroll','.definition-list','.timeline>article','.resource-list','.code-panel','.code-block','.measurement-meta','.end-box','.page-hero>.eyebrow','.page-hero>h1','.page-hero>p','.page-hero>.tag','.page-hero>.button-row'].join(',');
if(html.classList.contains('js-reveal')){
const targets=$$(revealList);
if('IntersectionObserver' in window){
const io=new IntersectionObserver(entries=>{
entries.forEach(entry=>{
if(!entry.isIntersecting)return;
entry.target.classList.add('in');
if(entry.target.style.getPropertyValue('--rd'))setTimeout(()=>entry.target.style.removeProperty('--rd'),1500);
io.unobserve(entry.target);
});
},{rootMargin:'0px 0px -6% 0px',threshold:.05});
const counts=new Map();
targets.forEach(el=>{
const parent=el.parentElement;
const i=counts.get(parent)||0;
counts.set(parent,i+1);
if(i)el.style.setProperty('--rd',Math.min(i,5)*70+'ms');
io.observe(el);
});
html.classList.add('js-ready');
}else{
html.classList.remove('js-reveal');
}
}else{
html.classList.add('js-ready');
}
if(hoverable.matches&&finePointer.matches){
$$('.feature-card,.domain-card,.score-card,.footer-repo').forEach(el=>{
el.addEventListener('pointermove',e=>{
const r=el.getBoundingClientRect();
el.style.setProperty('--mx',((e.clientX-r.left)/r.width*100).toFixed(1)+'%');
el.style.setProperty('--my',((e.clientY-r.top)/r.height*100).toFixed(1)+'%');
},{passive:true});
});
}
if(!reduce.matches){
const nums=$$('.stats strong,.score-number,.provider-row strong');
const runCounter=el=>{
const node=[...el.childNodes].find(n=>n.nodeType===3&&/\d/.test(n.nodeValue));
if(!node)return;
const raw=node.nodeValue,m=raw.match(/\d[\d,]*/);
if(!m)return;
const target=parseInt(m[0].replace(/[^\d]/g,''),10);
if(!isFinite(target)||target<2)return;
const t0=performance.now(),dur=1500;
const tick=now=>{
const p=Math.min(1,(now-t0)/dur),ease=1-Math.pow(1-p,3);
node.nodeValue=raw.replace(m[0],String(Math.round(target*ease)));
if(p<1)requestAnimationFrame(tick);else node.nodeValue=raw;
};
requestAnimationFrame(tick);
};
if('IntersectionObserver' in window){
const cio=new IntersectionObserver(es=>{
es.forEach(en=>{
if(!en.isIntersecting)return;
runCounter(en.target);
cio.unobserve(en.target);
});
},{threshold:.6});
nums.forEach(el=>cio.observe(el));
}
}
const track=$('.marquee-track');
if(track){
const frag=document.createDocumentFragment();
$$(':scope > *',track).forEach(el=>{
const clone=el.cloneNode(true);
clone.setAttribute('aria-hidden','true');
frag.append(clone);
});
track.append(frag);
const measure=()=>{
const half=track.scrollWidth/2;
if(!half)return;
track.style.setProperty('--marquee-shift','-'+Math.round(half)+'px');
track.style.setProperty('--marquee-dur',(half/46).toFixed(1)+'s');
};
measure();
addEventListener('load',measure,{once:true});
if(document.fonts&&document.fonts.ready)document.fonts.ready.then(measure).catch(()=>{});
}
const hero=$('.hero'),sculpture=$('.sculpture');
if(hero&&sculpture&&hoverable.matches&&finePointer.matches&&!reduce.matches){
let frame=0,pendingX=0,pendingY=0;
sculpture.style.transition='translate .45s cubic-bezier(.2,.7,.2,1)';
hero.addEventListener('pointermove',e=>{
if(html.dataset.motion==='paused')return;
const r=hero.getBoundingClientRect();
pendingX=((e.clientX-r.left)/r.width-.5)*-16;
pendingY=((e.clientY-r.top)/r.height-.5)*-12;
if(frame)return;
frame=requestAnimationFrame(()=>{
frame=0;
sculpture.style.translate=pendingX.toFixed(1)+'px '+pendingY.toFixed(1)+'px';
});
},{passive:true});
hero.addEventListener('pointerleave',()=>{sculpture.style.translate='0px 0px';});
}
}catch(error){html.classList.remove('js-reveal');}
})();
