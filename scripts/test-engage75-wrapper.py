from pathlib import Path
import tempfile,os,subprocess,hashlib,json,shutil,argparse,selectors,time
parser=argparse.ArgumentParser(description='Check the Engage 75 handoff with fake executables only.')
parser.add_argument('firmware',type=Path,help='Checked official 5.20.1 ZIP, read as data only')
parser.add_argument('--report',type=Path)
args=parser.parse_args()
root=Path(__file__).resolve().parent.parent
firmware=args.firmware.resolve()
assert hashlib.sha256(firmware.read_bytes()).hexdigest()=='210d0402470502aa28716a98a67a4343e6f29d98f0b7681b676e66a12d2c3a98','wrong fixture archive'
results=[]
with tempfile.TemporaryDirectory(prefix='engage75-wrapper-fixtures-') as tmp:
 base=Path(tmp)
 for case in ['cancel','eof','bad-binary','wrong-version','network-fail','bad-firmware','success','install-fail','linked-report']:
  bundle=base/(case+' folder with spaces');bundle.mkdir();tools=base/(case+' tools');tools.mkdir();trace=base/(case+'.trace')
  shutil.copyfile(root/'scripts/engage75-test.sh',bundle/'test-engage75.sh')
  fake=bundle/'jabridge';fake.write_text('''#!/usr/bin/env bash
set -eu
if [[ "$1" == --version ]];then
 if [[ "${MOCK_CASE}" == wrong-version ]];then printf 'Jabridge wrong\\n';else printf 'Jabridge 1.6.2-issue62.9\\n';fi
 exit 0
fi
[[ ( "$#" == 5 || ( "$#" == 6 && "$6" == --new-attempt ) ) && "$1" == firmware && "$2" == install && "$3" == "$MOCK_BUNDLE/Jabra_Engage_75_5.20.1.zip" && "$4" == --preview-engage75-legacy && "$5" == --i-accept-risk ]] || exit 71
printf 'install\\n' >> "$MOCK_TRACE"
if [[ "$#" == 6 ]];then printf 'new-attempt-flag\\n' >> "$MOCK_TRACE";fi
printf 'Simulated installer output\\n'
exit "${MOCK_INSTALL_CODE:-0}"
''')
  curl=tools/'curl';curl.write_text('''#!/usr/bin/env bash
set -eu
printf 'download\\n' >> "$MOCK_TRACE"
out=''
while [[ $# -gt 0 ]];do
 if [[ "$1" == --output ]];then out="$2";shift 2;else shift;fi
done
[[ -n "$out" ]] || exit 72
if [[ "$MOCK_CASE" == network-fail ]];then printf 'Simulated network failure\\n' >&2;exit 22;fi
if [[ "$MOCK_CASE" == bad-firmware ]];then printf 'wrong bytes' > "$out";else cp -- "$MOCK_FIRMWARE" "$out";fi
''');curl.chmod(0o700)
  (bundle/'SHA256SUMS').write_text(''.join(hashlib.sha256((bundle/name).read_bytes()).hexdigest()+'  '+name+'\n' for name in ['jabridge','test-engage75.sh']))
  if case=='bad-binary':fake.write_text(fake.read_text()+'# changed bytes\n')
  if case=='linked-report':(bundle/'jabridge-engage75-result.txt').symlink_to(base/'untouched')
  env={**os.environ,'PATH':str(tools)+os.pathsep+os.environ['PATH'],'MOCK_CASE':case,'MOCK_TRACE':str(trace),'MOCK_BUNDLE':str(bundle),'MOCK_FIRMWARE':str(firmware),'MOCK_INSTALL_CODE':'9' if case=='install-fail' else '0'}
  answer='\n\n' if case=='cancel' else '' if case=='eof' else '\nUPDATE\n'
  proc=subprocess.run(['bash',str(bundle/'test-engage75.sh')],input=answer,text=True,capture_output=True,env=env,timeout=20)
  calls=trace.read_text().splitlines() if trace.exists() else []
  marker=(bundle/'.engage75-test-started').exists()
  assert not (bundle/'.engage75-test-running').exists(),'normal exit leaked active ownership'
  if case in ['cancel','eof']:
   assert proc.returncode==0 and not calls and not marker,(case,proc.stdout,proc.stderr,calls)
  elif case in ['bad-binary','wrong-version','linked-report']:
   assert proc.returncode==1 and not calls and not marker,(case,proc.stdout,proc.stderr,calls)
  elif case in ['network-fail','bad-firmware']:
   assert proc.returncode==1 and calls==['download'] and marker,(case,proc.stdout,proc.stderr,calls)
  else:
   assert proc.returncode==(9 if case=='install-fail' else 0) and calls==['download','install'] and marker,(case,proc.stdout,proc.stderr,calls)
   report=(bundle/'jabridge-engage75-result.txt').read_bytes();assert b'Simulated installer output' in report
   rerun=subprocess.run(['bash',str(bundle/'test-engage75.sh')],input='\nUPDATE\n',text=True,capture_output=True,env=env,timeout=20)
   assert rerun.returncode==1 and trace.read_text().splitlines()==calls and (bundle/'jabridge-engage75-result.txt').read_bytes()==report
   if case=='success':
    declined=subprocess.run(['bash',str(bundle/'test-engage75.sh')],input='NEW ATTEMPT\n\n',text=True,capture_output=True,env=env,timeout=20)
    assert declined.returncode==0 and trace.read_text().splitlines()==calls
    retry=subprocess.run(['bash',str(bundle/'test-engage75.sh')],input='NEW ATTEMPT\nUPDATE\n',text=True,capture_output=True,env=env,timeout=20)
    assert retry.returncode==0,(retry.stdout,retry.stderr)
    assert (bundle/'jabridge-engage75-result.txt').read_bytes()==report
    assert len(list(bundle.glob('jabridge-engage75-result-new.*.txt')))==1
    assert trace.read_text().splitlines()==calls+['download','install','new-attempt-flag']

  results.append({'case':case,'exit':proc.returncode,'calls':calls,'passed':True})
 # Reproduce the review's exact two-window ordering: both instances start
 # before acceptance; first finishes; second receives late UPDATE. It must
 # never overwrite the successful owner's report or replace its archive.
 case='concurrent'
 bundle=base/'two concurrent windows';bundle.mkdir();tools=base/'concurrent tools';tools.mkdir();trace=base/'concurrent.trace'
 shutil.copyfile(root/'scripts/engage75-test.sh',bundle/'test-engage75.sh')
 # Reuse the last fixture's fake commands, with fresh paths/trace.
 shutil.copyfile(fake,bundle/'jabridge');shutil.copyfile(curl,tools/'curl');(tools/'curl').chmod(0o700)
 (bundle/'SHA256SUMS').write_text(''.join(hashlib.sha256((bundle/name).read_bytes()).hexdigest()+'  '+name+'\n' for name in ['jabridge','test-engage75.sh']))
 env={**os.environ,'PATH':str(tools)+os.pathsep+os.environ['PATH'],'MOCK_CASE':case,'MOCK_TRACE':str(trace),'MOCK_BUNDLE':str(bundle),'MOCK_FIRMWARE':str(firmware),'MOCK_INSTALL_CODE':'0'}
 def observe_prompt_or_exit(proc):
  data=b'';selector=selectors.DefaultSelector();selector.register(proc.stdout,selectors.EVENT_READ)
  limit=time.monotonic()+10
  while time.monotonic()<limit:
   if b'Type UPDATE' in data:selector.close();return data,True
   if proc.poll() is not None:
    data+=proc.stdout.read();selector.close();return data,False
   for key,_ in selector.select(0.1):
    chunk=os.read(key.fd,4096)
    if chunk:data+=chunk
  selector.close();raise AssertionError('helper did not reach prompt or finish')
 first=subprocess.Popen(['bash',str(bundle/'test-engage75.sh')],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,env=env)
 second=None
 try:
  first.stdin.write(b'\n');first.stdin.flush()
  first_output,first_prompt=observe_prompt_or_exit(first);assert first_prompt,first_output
  second=subprocess.Popen(['bash',str(bundle/'test-engage75.sh')],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,env=env)
  second_output,second_prompt=observe_prompt_or_exit(second)
  # In the old helper second_prompt was true. Do not fail early: finish the
  # interleaving so the regression witnesses actual report/archive preservation.
  first_rest,_=first.communicate(b'UPDATE\n',timeout=20)
  assert first.returncode==0,(first_output,first_rest)
  report=bundle/'jabridge-engage75-result.txt';archive=bundle/'Jabra_Engage_75_5.20.1.zip'
  before={p.name:(p.read_bytes(),p.stat().st_ino,p.stat().st_mtime_ns) for p in [report,archive]}
  second_rest,_=second.communicate(b'UPDATE\n',timeout=20)
  after={p.name:(p.read_bytes(),p.stat().st_ino,p.stat().st_mtime_ns) for p in [report,archive]}
  assert before==after,'second window replaced the first result/archive'
  assert trace.read_text().splitlines()==['download','install'],'second window downloaded or installed again'
  assert second.returncode==1 and not second_prompt,(second_output,second_rest)
  assert b'already open' in second_output+second_rest
  assert not (bundle/'.engage75-test-running').exists()
  results.append({'case':'concurrent-two-window','first_exit':first.returncode,'second_exit':second.returncode,'second_prompt':second_prompt,'report_and_archive_identity_preserved':before==after,'calls':['download','install'],'passed':True})
 finally:
  for proc in [first,second]:
   if proc is not None and proc.poll() is None:proc.kill();proc.wait()
if args.report:args.report.write_text(json.dumps({'hardware_used':False,'fake_installer_only':True,'tests':results,'passed':True},indent=2)+'\n')
print(f'All{len(results)} wrapper cases passed using fake executables; no hardware or services used.')
