"""Exercise real CRM -> MCP project RAG -> DeepSeek; keeps credentials out of reports."""
import argparse
import json
import re
import time
import urllib.request
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--url', default='http://127.0.0.1:8090')
parser.add_argument('--test-auth', action='store_true', help='Use the disposable local validation token')
parser.add_argument('--only', default='', help='Comma-separated ticket IDs, or general')
parser.add_argument('--output', default='.local/live-results.json')
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
values = dict(line.split('=', 1) for line in (root / '.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
token = 'ui-validation-token-for-local-only-2026' if args.test_auth else values['SUPPORT_ACCESS_TOKEN']
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

def request(path, payload=None):
    data = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
    req = urllib.request.Request(args.url + path, data=data, headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    with opener.open(req, timeout=165) as response:
        return json.load(response)

cases = [
    ('general', 'Расскажи о проекте', r'проект|приложени|Go'),
    ('SUP-001', 'Почему это происходит и что делать дальше?', r'удал|Windows|Ollama|каталог'),
    ('SUP-002', 'Почему это происходит и что делать дальше?', r'индекс|reindex'),
    ('SUP-003', 'Почему это происходит и что делать дальше?', r'тайм|60|ожидан'),
    ('SUP-004', 'Почему это происходит и что делать дальше?', r'окн|последн|архив'),
    ('SUP-005', 'Почему это происходит и что делать дальше?', r'цитат|доказатель|провер'),
    ('SUP-006', 'Почему это происходит и что делать дальше?', r'токен|авторизац|прав'),
]
if args.only:
    selected = set(args.only.split(','))
    cases = [c for c in cases if c[0] in selected]
report = {'createdAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'results': []}
for ticket_id, question, expected in cases:
    started = time.monotonic()
    try:
        payload = {'question': question}
        if ticket_id != 'general':
            payload['ticketId'] = ticket_id
        result = request('/api/chat', payload)
        checks = {
            'answerRelevant': bool(re.search(expected, result.get('answer', ''), re.I)),
            'hasDocumentEvidence': any(s.get('kind') == 'document' for s in result.get('sources', [])),
            'realRAG': result.get('trace', {}).get('retrieval') == 'rag',
            'crmViaMCP': ticket_id == 'general' or result.get('trace', {}).get('crm') == 'mcp',
            'model': result.get('trace', {}).get('model') == 'deepseek-flash',
        }
        item = {'id': ticket_id, 'question': question, 'seconds': round(time.monotonic() - started, 2), 'checks': checks, 'response': result}
        print(json.dumps({'id': ticket_id, 'checks': checks, 'seconds': item['seconds'], 'answerPreview': result.get('answer', '')[:160]}, ensure_ascii=False), flush=True)
    except Exception as error:
        item = {'id': ticket_id, 'failed': type(error).__name__}
        print(json.dumps(item), flush=True)
    report['results'].append(item)
    output = root / args.output
    output.parent.mkdir(parents=True, exist_ok=True)
    report['passed'] = all(all(i.get('checks', {'failed': False}).values()) for i in report['results'])
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
raise SystemExit(0 if report['passed'] else 1)
