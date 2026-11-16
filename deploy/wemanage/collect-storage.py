#!/usr/bin/env python3
"""Collect lightweight storage/backup metadata; never read or delete log contents."""
import datetime,json,os,subprocess
from pathlib import Path
home=Path.home();folder=home/'gateway-backups'
pg=['psql','host=127.0.0.1 port=15432 user=gateway dbname=gateway sslmode=require','-X','-q','-A','-t','-v','ON_ERROR_STOP=1']
def sql(s):return subprocess.check_output(pg,input=s,universal_newlines=True).strip()
stat=os.statvfs(home)
backups=list(folder.glob('gateway-*.sql.gz'))
latest=max(backups,key=lambda x:x.stat().st_mtime) if backups else None
log=folder/'backup.log';last=''
if log.exists():
 with log.open('rb') as f:
  f.seek(max(0,log.stat().st_size-8192))
  lines=[s for s in f.read().decode('utf-8','replace').splitlines() if s.strip()]
  last=lines[-1] if lines else ''
state='未记录'
if 'backup blocked' in last:state='空间保护拦截，尚未生成新备份'
elif 'backup ok' in last:state='最近一次任务生成备份成功；尚未做恢复演练'
elif 'No space left' in last:state='空间不足，任务失败'
elif last:state='请核查任务日志'
detail={'total_bytes':stat.f_blocks*stat.f_frsize,'available_bytes':stat.f_bavail*stat.f_frsize,'database_bytes':int(sql('select pg_database_size(current_database());')),'backup_file':latest.name if latest else None,'backup_bytes':latest.stat().st_size if latest else None,'backup_at':datetime.datetime.fromtimestamp(latest.stat().st_mtime,datetime.timezone.utc).isoformat() if latest else None,'backup_state':state,'backup_last_log':last.replace(str(home),'~'),'mac_backup_connected':False}
raw=json.dumps(detail,ensure_ascii=False).replace("'","''")
sql("insert into wm_observation_sync(name,at,detail)values('storage',now(),'"+raw+"'::jsonb) on conflict(name)do update set at=excluded.at,detail=excluded.detail;")
print('Storage snapshot saved')
