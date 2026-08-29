import json
DD = r'C:\var\lib\quant-trading-v2'
a = json.load(open(DD + '/auth.json'))
gid = 'u_1788013695729247700'
for u in a['users']:
    if u.get('id') == gid and u.get('username') == 'admin':
        print('renaming admin -> admin_gz')
        u['username'] = 'admin_gz'
json.dump(a, open(DD + '/auth.json', 'w'), ensure_ascii=False, indent=2)
print('users:', [u.get('username') for u in a['users']])
