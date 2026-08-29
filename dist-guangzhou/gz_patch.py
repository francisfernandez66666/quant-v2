import json, glob, os, tarfile

baks = sorted(glob.glob(r'C:\var\lib\quant-trading-v2.bak.*.tgz'), reverse=True)
bak = baks[0]
tmp = r'C:\var\lib\gzpatch_tmp'
os.makedirs(tmp, exist_ok=True)
with tarfile.open(bak) as t:
    t.extractall(tmp)

def find(name):
    for root, _, files in os.walk(tmp):
        if name in files:
            return os.path.join(root, name)
    return None

p = find('config.json')
gzcfg = json.load(open(p)) if p else {}
a = find('auth.json')
gzauth = json.load(open(a)) if a else {}

DD = r'C:\var\lib\quant-trading-v2'
newcfg = json.load(open(os.path.join(DD, 'config.json')))
newcfg['qmt'] = gzcfg.get('qmt', newcfg.get('qmt'))
rules = newcfg.setdefault('rules', {})
if isinstance(gzcfg.get('rules'), dict):
    if 'qmt' in gzcfg['rules']:
        rules['qmt'] = gzcfg['rules']['qmt']
    if 'llm' in gzcfg['rules']:
        rules['llm'] = gzcfg['rules']['llm']
json.dump(newcfg, open(os.path.join(DD, 'config.json'), 'w'), ensure_ascii=False, indent=2)

newauth = json.load(open(os.path.join(DD, 'auth.json')))
gzadmin = next((u for u in gzauth.get('users', []) if u.get('username') == 'admin'), None)
if gzadmin and not any(u.get('id') == gzadmin.get('id') for u in newauth.get('users', [])):
    newauth['users'].append(gzadmin)
if isinstance(gzauth.get('configs'), dict) and isinstance(newauth.get('configs'), dict):
    for k, v in gzauth['configs'].items():
        if k not in newauth['configs']:
            newauth['configs'][k] = v
newauth['schema_version'] = gzauth.get('schema_version', newauth.get('schema_version'))
json.dump(newauth, open(os.path.join(DD, 'auth.json'), 'w'), ensure_ascii=False, indent=2)
print('patched. users:', [u.get('username') for u in newauth.get('users', [])])
print('gz admin kept:', bool(gzadmin))
