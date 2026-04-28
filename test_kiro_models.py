import json, urllib.request, ssl

with open(r'C:\Users\gool\.agents\chatbot.json') as f:
    cfg = json.load(f)

token = None
profile_arn = None
for p in cfg['Agents.ChatBot']['Providers']:
    if p['Name'] == 'kiro':
        token = p['Extra']['accessToken']
        profile_arn = p['Extra']['profileArn']
        break

print(f"Token prefix: {token[:30]}...")
print(f"Profile ARN: {profile_arn}")

body = json.dumps({"origin": "KIRO_CLI", "profileArn": profile_arn}).encode()
req = urllib.request.Request(
    "https://q.us-east-1.amazonaws.com/",
    data=body,
    headers={
        "Content-Type": "application/x-amz-json-1.0",
        "x-amz-target": "AmazonCodeWhispererService.ListAvailableModels",
        "Authorization": f"Bearer {token}",
        "x-amzn-codewhisperer-optout": "false"
    }
)
ctx = ssl.create_default_context()
try:
    resp = urllib.request.urlopen(req, context=ctx, timeout=15)
    data = json.loads(resp.read())
    print(json.dumps(data, indent=2))
except urllib.error.HTTPError as e:
    print(f"HTTP {e.code}: {e.read().decode()}")
except Exception as e:
    print(f"Error: {e}")
