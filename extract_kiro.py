import json, sys

with open(r'C:\Users\gool\.agents\chatbot.json', 'r') as f:
    cfg = json.load(f)

kiro = None
for p in cfg.get('Agents.ChatBot', {}).get('Providers', []):
    if p.get('Name') == 'kiro':
        kiro = p
        break

if not kiro:
    print('ERROR: kiro provider not found')
    sys.exit(1)

extra = kiro.get('Extra', {})
auth = {
    'type': 'kiro',
    'token': {
        'access_token': extra.get('accessToken', ''),
        'refresh_token': extra.get('refreshToken', ''),
        'expires_at': extra.get('expiresAt', '')
    },
    'auth_method': extra.get('authMethod', 'sso'),
    'client_id': extra.get('clientId', ''),
    'client_secret': extra.get('clientSecret', ''),
    'profile_arn': extra.get('profileArn', ''),
    'idc_region': extra.get('idcRegion', 'ap-southeast-1')
}
with open(r'C:\Users\gool\repos\CLIProxyAPI\kiro-sso-fresh.json', 'w') as f:
    json.dump(auth, f)
print('OK, expires_at:', auth['token']['expires_at'])
