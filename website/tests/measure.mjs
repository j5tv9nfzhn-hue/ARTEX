import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import lighthouse from 'lighthouse';
import {launch} from 'chrome-launcher';
import {chromium} from '@playwright/test';
import desktopConfig from 'lighthouse/core/config/lr-desktop-config.js';
const base=process.env.SITE_BASE||'http://127.0.0.1:4173';
const browserReport=JSON.parse(await fs.readFile('reports/browser.json','utf8'));
const s=browserReport.summary;if(s.violations||s.errors||s.overflows||s.failedInteractions||s.brokenLinks)throw new Error('Browser checks must pass before publishing measurements.');
const summary={completed:true,measuredAt:new Date().toISOString(),environment:`${os.platform()} ${os.arch()} sandbox; Node ${process.version}; local loopback HTTP; gzip enabled; no Cloudflare or ARTEX backend`,browser:s,browserVersion:browserReport.browserVersion,lighthouseVersion:'12.8.2',lighthouse:{},subjectHash:''};
for(const type of ['mobile','desktop']){
 const chrome=await launch({chromePath:chromium.executablePath(),chromeFlags:['--headless','--no-sandbox','--disable-dev-shm-usage','--disable-gpu']});
 try{
  console.log('Measuring',type);
  const result=await lighthouse(base+'/',{port:chrome.port,output:['html','json'],logLevel:'error',onlyCategories:['performance','accessibility','best-practices','seo']},type==='desktop'?desktopConfig:undefined);
  if(result.lhr.runtimeError)throw new Error(JSON.stringify(result.lhr.runtimeError));
  await fs.writeFile(`reports/lighthouse-${type}.html`,result.report[0]);await fs.writeFile(`reports/lighthouse-${type}.json`,result.report[1]);
  const l=result.lhr,score=id=>Math.round((l.categories[id]?.score??0)*100),audit=id=>l.audits[id]?.numericValue??0;
  summary.lighthouse[type]={performance:score('performance'),accessibility:score('accessibility'),bestPractices:score('best-practices'),seo:score('seo'),lcpMs:audit('largest-contentful-paint'),cls:audit('cumulative-layout-shift'),tbtMs:audit('total-blocking-time'),fcpMs:audit('first-contentful-paint'),fetchTime:l.fetchTime,formFactor:l.configSettings.formFactor,throttling:l.configSettings.throttling,screenEmulation:l.configSettings.screenEmulation,userAgent:l.userAgent,warnings:l.runWarnings};
  console.log(type,JSON.stringify(summary.lighthouse[type]));
 }finally{await chrome.kill();}
}
async function walk(dir,relative=''){let out=[];for(const entry of (await fs.readdir(dir,{withFileTypes:true})).sort((a,b)=>a.name.localeCompare(b.name))){const rel=path.posix.join(relative,entry.name);if(rel.startsWith('benchmarks/')||rel.startsWith('reports/'))continue;if(entry.isDirectory())out.push(...await walk(path.join(dir,entry.name),rel));else out.push({path:rel,sha256:crypto.createHash('sha256').update(await fs.readFile(path.join(dir,entry.name))).digest('hex')});}return out;}
const manifest=await walk('site');summary.subjectHash=crypto.createHash('sha256').update(JSON.stringify(manifest)).digest('hex');await fs.writeFile('reports/measured-manifest.json',JSON.stringify(manifest,null,2));await fs.writeFile('reports/measurements.json',JSON.stringify(summary,null,2));
await fs.mkdir('site/reports',{recursive:true});for(const f of ['measurements.json','browser.json','measured-manifest.json','lighthouse-mobile.html','lighthouse-mobile.json','lighthouse-desktop.html','lighthouse-desktop.json'])await fs.copyFile('reports/'+f,'site/reports/'+f);
console.log('Measurement reports saved to reports/ and site/reports/.');
