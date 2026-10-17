from pathlib import Path
import os,json,subprocess,time,traceback
root=Path(__file__).resolve().parents[1];state=root/'deployment'/'status.json';log=root/'deployment'/'deploy.log';site=root/'site'
def save(**data):state.write_text(json.dumps({'updated_at':time.time(),**data}),encoding='utf-8')
p=None
try:
 env=dict(os.environ);env['CI']='true';env['WRANGLER_SEND_METRICS']='false'
 if not (site/'index.html').is_file():raise RuntimeError('site/index.html missing; nothing to deploy')
 save(stage='uploading')
 with log.open('wb') as out:
  p=subprocess.Popen(['cmd','/c','wrangler pages deploy site --project-name artex-official-website --branch main'],cwd=root,env=env,stdout=out,stderr=out,stdin=subprocess.DEVNULL,creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
  try:code=p.wait(timeout=180)
  except subprocess.TimeoutExpired:
   subprocess.run(['taskkill','/PID',str(p.pid),'/T','/F'],stdout=out,stderr=out,timeout=10)
   save(stage='unknown_timeout',message='Do not retry without checking Cloudflare deployment status');raise SystemExit(0)
 save(stage='finished' if code==0 else 'failed',exit_code=code)
except Exception as e:save(stage='failed',message=str(e),trace=traceback.format_exc())
