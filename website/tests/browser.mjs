import {chromium} from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const base=process.env.SITE_BASE||'http://127.0.0.1:4173';
const routes=JSON.parse(await fs.readFile('site/routes.json','utf8'));
const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
const output={measuredAt:new Date().toISOString(),browserVersion:browser.version(),routes:[...routes,'/missing-example/'],cases:[],interactions:[],linkFailures:[],environment:'Linux sandbox; local Node HTTP preview; Chromium; no ARTEX backend or Cloudflare connection'};
await fs.mkdir('reports/screenshots',{recursive:true});
const allLinks=new Set();
try{
for(const viewport of [{width:1440,height:1000},{width:390,height:844}]){
 for(const theme of ['dark','light']){
  const context=await browser.newContext({viewport,reducedMotion:'reduce',colorScheme:theme});
  await context.addInitScript(theme=>localStorage.setItem('artex-theme',theme),theme);
  for(const route of [...routes,'/missing-example/']){
   const page=await context.newPage();const errors=[];
   page.on('pageerror',e=>errors.push(e.message));
   page.on('console',m=>{if(m.type()==='error'&&!(route==='/missing-example/'&&m.text().includes('404')))errors.push(m.text());});
   page.setDefaultTimeout(8000);const response=await page.goto(base+route,{waitUntil:'networkidle'});await page.evaluate(()=>document.fonts.ready);
   const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);
   const headingCount=await page.locator('h1').count();
   const axe=await new AxeBuilder({page}).withTags(['wcag2a','wcag2aa','wcag21a','wcag21aa']).analyze();
   const violations=axe.violations.map(v=>({id:v.id,impact:v.impact,description:v.description,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))}));
   output.cases.push({route,viewport,theme,status:response.status(),overflow,headingCount,errors,violations});
   (await page.locator('a[href]').evaluateAll(nodes=>nodes.map(a=>a.href))).filter(h=>h.startsWith(base)&&!h.includes('/missing-example/')).forEach(h=>allLinks.add(h));
   if(route==='/')await page.screenshot({path:`reports/screenshots/home-${theme}-${viewport.width}.png`,fullPage:true});
   if(route==='/workflows/'&&viewport.width===1440&&theme==='dark')await page.screenshot({path:'reports/screenshots/workflows-dark.png',fullPage:true});
   console.log(route,viewport.width,theme,'axe',violations.length,'overflow',overflow,'errors',errors.length);
   await page.close();
  }
  await context.close();
 }
}
const context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(8000);
async function check(name,fn){try{await fn();output.interactions.push({name,passed:true});}catch(e){output.interactions.push({name,passed:false,error:e.message});}}
await page.goto(base+'/',{waitUntil:'networkidle'});
await check('品牌视角切换',async()=>{await page.locator('[data-perspective="reverse"]').click();assert.match(await page.locator('h1').innerText(),/读懂逻辑/);await page.locator('[data-perspective="joint"]').click();});
await check('主题切换与跨路由持久化',async()=>{await page.evaluate(()=>localStorage.setItem('artex-theme','light'));await page.reload();await page.locator('button[data-theme]').click();const mode=await page.locator('html').getAttribute('data-themePreference');await page.goto(base+'/architecture/');assert.equal(await page.locator('html').getAttribute('data-themePreference'),mode);});
await check('命令搜索与键盘导航',async()=>{await page.keyboard.press('Control+k');await page.locator('#site-search').fill('逆向');await page.waitForTimeout(150);assert.ok(await page.locator('.search-result').count()>0);await page.keyboard.press('ArrowDown');assert.equal(await page.evaluate(()=>document.activeElement.className),'search-result');await page.keyboard.press('Escape');assert.equal(await page.locator('#search-dialog').evaluate(n=>n.open),false);});
await check('搜索无结果反馈',async()=>{await page.locator('.search-button').click();await page.locator('#site-search').fill('zzzzzz-not-found');await page.waitForTimeout(100);assert.ok(await page.locator('.search-empty').isVisible());await page.keyboard.press('Escape');});
await check('交接检查点与重置',async()=>{await page.goto(base+'/workflows/');await page.locator('[data-node="network"]').click();assert.match(await page.locator('[data-detail-state]').innerText(),/等待交接/);await page.locator('[data-node="handoff"]').click();await page.locator('[data-demo-action]').click();assert.match(await page.locator('[data-detail-state]').innerText(),/交接已确认/);await page.locator('[data-demo-action]').click();assert.match(await page.locator('[data-detail-state]').innerText(),/依赖已解除/);await page.locator('[data-reset]').click();assert.match(await page.locator('[data-detail-state]').innerText(),/静态分析完成/);});
await check('能力筛选',async()=>{await page.goto(base+'/platform/reverse/');await page.locator('[data-filter="pending"]').click();assert.equal(await page.locator('[data-capability]:visible').count(),2);await page.locator('[data-filter="source"]').click();assert.equal(await page.locator('[data-capability]:visible').count(),4);});
await check('FAQ 展开',async()=>{await page.goto(base+'/trust/');await page.locator('summary').first().click();assert.equal(await page.locator('details').first().getAttribute('open'),'');});
await check('动效偏好持久化',async()=>{await page.locator('button[data-motion]').click();const paused=await page.locator('html').getAttribute('data-motion');await page.goto(base+'/');assert.equal(await page.locator('html').getAttribute('data-motion'),paused);assert.equal(await page.locator('h1').count(),1);});
await check('移动端导航',async()=>{await page.setViewportSize({width:390,height:844});await page.locator('.mobile-menu').click();assert.equal(await page.locator('.mobile-menu').getAttribute('aria-expanded'),'true');await page.locator('#nav-links a[href$="docs/index.html"]').click();assert.match(page.url(),/\/docs\//);});
await check('文档复制按钮可操作',async()=>{await context.grantPermissions(['clipboard-read','clipboard-write'],{origin:base});await page.locator('[data-copy="website-start"]').click();assert.match(await page.evaluate(()=>navigator.clipboard.readText()),/npm run preview/);});
for(const href of allLinks){const url=new URL(href,base),display=url.pathname+url.hash,response=await context.request.get(url.href);if(response.status()!==200)output.linkFailures.push({href:display,status:response.status()});if(url.hash&&response.status()===200&&response.headers()['content-type']?.includes('text/html')){const body=await response.text(),id=decodeURIComponent(url.hash.slice(1));if(!body.includes(`id="${id}"`))output.linkFailures.push({href:display,error:'anchor not found'});}}
await check('未知路由返回真实 404',async()=>{const res=await context.request.get(base+'/missing-example/');assert.equal(res.status(),404);assert.match(await res.text(),/这里，还没有路径/);});
await context.close();
}finally{await browser.close();output.summary={pages:output.routes.length,combinations:output.cases.length,violations:output.cases.reduce((n,c)=>n+c.violations.length,0),errors:output.cases.reduce((n,c)=>n+c.errors.length,0),overflows:output.cases.filter(c=>c.overflow).length,failedInteractions:output.interactions.filter(i=>!i.passed).length,brokenLinks:output.linkFailures.length};await fs.writeFile('reports/browser.json',JSON.stringify(output,null,2));console.log(JSON.stringify(output.summary,null,2));}
const fail=output.summary.violations+output.summary.errors+output.summary.overflows+output.summary.failedInteractions+output.summary.brokenLinks+output.cases.filter(c=>c.headingCount!==1||c.status!==(c.route==='/missing-example/'?404:200)).length;process.exitCode=fail?1:0;
