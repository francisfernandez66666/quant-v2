# fix_updater_path.ps1 - point config.xt.json carrier_updater_script at the
# repo-current update script inside qmt_gateway (was: stale Downloads copy).
# Backup first; python round-trips the JSON (UTF-8, keys preserved).
$py = "C:\Users\57516\AppData\Local\Python\pythoncore-3.15-64\python.exe"
$cfg = "C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json"
$code = "import json,shutil; p=r'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json'; shutil.copy2(p, p+'.bak_20261116'); d=json.load(open(p,encoding='utf-8')); d['carrier_updater_script']=r'C:\qmt\quant-trading-v2\qmt_gateway\update_qmt_strategy.ps1'; json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2); print('CFG_UPDATED to', d['carrier_updater_script'])"
& $py -c $code
