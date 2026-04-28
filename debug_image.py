import json, urllib.request
BASE = 'http://192.168.2.7:8317'
KEY = 'sk-cap-9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c'
HDRS = {'Authorization': 'Bearer ' + KEY, 'Content-Type': 'application/json'}
img_url = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/58BHgAI/AL+hc2rNAAAAABJRU5ErkJggg=='
img_part = dict(type='image_url')
img_part['image_url'] = dict(url=img_url)
txt_part = dict(type='text', text='What color is this 1x1 pixel image? Reply with just the color.')
messages = [{'role': 'user', 'content': [txt_part, img_part]}]
body = json.dumps({'model': 'claude-opus-4.6', 'messages': messages, 'max_tokens': 50}).encode()
r = urllib.request.Request(BASE + '/v1/chat/completions', data=body, headers=HDRS)
resp = urllib.request.urlopen(r, timeout=45)
d = json.loads(resp.read())
print('status:', resp.status)
print('full response:', json.dumps(d, indent=2))
c = d.get('choices', [{}])[0].get('message', {}).get('content', '')
print('content repr:', repr(c))
print('content len:', len(c))
