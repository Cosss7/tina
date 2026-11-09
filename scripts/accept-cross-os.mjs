// Manual Windows + WSL acceptance. Builds are explicit; no external model calls.
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdirSync, mkdtempSync, writeFileSync, readFileSync, unlinkSync } from 'node:fs';
import path from 'node:path';
import { createServer } from 'node:net';

assert.equal(process.platform, 'win32', 'Run on Windows with WSL available');
const root = process.cwd(); const distro = process.env.TINA_WSL_DISTRO || 'arch';
const directory = mkdtempSync(path.join(root, '.build', 'cross-os-'));
const linuxDirectory = '/tmp/tina-cross-' + randomUUID();
const wsl = (...args) => execFileSync('wsl.exe', ['-d', distro, '--exec', ...args], { encoding: 'utf8' }).trim();
const linuxPath = (filename) => '/mnt/' + filename[0].toLowerCase() + filename.slice(2).replaceAll('\\', '/');
const server = createServer(); await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const port = server.address().port; await new Promise((resolve) => server.close(resolve));
const base = 'http://127.0.0.1:' + port; const db = path.join(directory, 'control.db');
const children = []; const files = []; let log = '';
const start = (binary, args) => {
 const child = spawn(binary, args, { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] }); children.push(child);
 child.on('error', (error) => { log += error.message; });
 child.stdout.on('data', (data) => { log += data.toString(); }); child.stderr.on('data', (data) => { log += data.toString(); }); return child;
};
const request = async (url, body, method = 'POST') => {
 const response = await fetch(base+url, body === undefined ? {} : {method, headers:{'content-type':'application/json'},body:JSON.stringify(body)});
 assert.ok(response.ok, url+': '+await response.clone().text()); return await response.json();
};
const wait = async (probe) => {
 const end = Date.now()+20000;
 while(Date.now()<end) { const value=await probe(); if(value) return value; await new Promise((resolve)=>setTimeout(resolve,50)); }
 throw new Error('cross OS timeout\n'+log);
};
try {
 wsl('/bin/mkdir', '-m', '700', linuxDirectory);
 const configs = [
  {nodeId:'router-windows',name:'Windows Router',database:path.join(directory,'router.db'),roots:[root],role:'router',cwd:root},
  {nodeId:'worker-linux',name:'WSL Linux Worker',database:linuxDirectory+'/journal.db',roots:[linuxDirectory],role:'worker',cwd:linuxDirectory},
 ];
 for (const local of configs) {
  const filename=path.join(directory,local.role+'.json'); files.push(filename);
  const config={nodeId:local.nodeId,name:local.name,token:randomBytes(32).toString('hex'),url:base.replace('http:','ws:')+'/ws/tinad',database:local.database,maxConcurrent:2,roots:local.roots,agents:[{id:local.role+'-proj-website',projectId:'proj-website',role:local.role,engineId:'fake'}]};
  writeFileSync(filename,JSON.stringify(config),{mode:0o600});
  execFileSync(path.join(root,'.build/control-plane.exe'),['-db',db,'-enroll-config',filename],{stdio:'pipe'});
 }
 start(path.join(root,'.build/control-plane.exe'),['-db',db,'-addr','127.0.0.1:'+port]);
 await wait(async()=>{try{return (await fetch(base+'/health')).ok;}catch{return false;}});
 assert.ok(wsl('/usr/bin/curl','--noproxy','*','-fsS',base+'/health').includes('true'),'WSL must reach Windows host loopback; configure mirrored networking or adjust script for your host IP');
 for (const local of configs) await request('/api/projects/proj-website/agents/'+local.role+'-proj-website',{name:local.name,engineId:'fake',nodeId:local.nodeId,cwd:local.cwd},'PUT');
 start(path.join(root,'.build/tinad.exe'),['-config',files[0]]);
 start('wsl.exe',['-d',distro,'--exec',linuxPath(path.join(root,'.build/tinad-linux')),'-config',linuxPath(files[1])]);
 await wait(async()=>(await request('/api/state')).nodes.filter((node)=>node.ready).length===2);
 const projectId='proj-website'; const demand=await request('/api/cards',{projectId,title:'跨 Windows/Linux 的闭环'});
 await request('/api/plans',{projectId,taskId:demand.id});
 const board=()=>request('/api/board?project='+projectId);
 const approval=await wait(async()=>(await board()).approvals[0]);
 const plan=await request('/api/plans/'+approval.id+'/decide',{projectId,decision:'approved'});
 for(const task of plan.tasks) {
  await request('/api/tasks/'+task.id+'/dispatch',{projectId});
  const card=await wait(async()=>(await board()).cards.find((card)=>card.id===task.id&&card.status==='review'));
  assert.ok(card.runs.every((run)=>run.nodeId==='worker-linux'));
  if(card.latestReport.state==='done') await request('/api/tasks/'+task.id+'/review',{projectId,decision:'approved'});
 }
 const final=await board(); assert.equal(final.cards.find((card)=>card.id===demand.id).runs[0].nodeId,'router-windows');
 const evidence={at:new Date().toISOString(),linux:wsl('/usr/bin/uname','-srm'),windows:process.platform,projectId,board:final};
 writeFileSync(path.join(directory,'evidence.json'),JSON.stringify(evidence,null,2));
 console.log('PASS Windows Router + WSL Linux Worker; separate journals; full approval/report/review loop. Evidence: '+directory);
} finally {
 for(const filename of files) {try { const config=JSON.parse(readFileSync(filename,'utf8'));config.stopping=true;writeFileSync(filename,JSON.stringify(config)); }catch{}}
 const nodes=children.slice(1);
 for(const child of nodes) if(child.exitCode===null&&child.signalCode===null) {
  const timer=setTimeout(()=>child.kill(),9000);await once(child,'exit');clearTimeout(timer);
 }
 for(const child of children) if(child.exitCode===null&&child.signalCode===null) { const done=once(child,'exit'); child.kill();await done; }
 for(const filename of files) {try{unlinkSync(filename);}catch{}}
 assert.ok(linuxDirectory.startsWith('/tmp/tina-cross-')&&!linuxDirectory.includes('..'));
 wsl('/bin/rm','-rf','--',linuxDirectory);
}
