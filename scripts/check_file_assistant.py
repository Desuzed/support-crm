"""Repeat two goal-level tasks through the existing CRM chat and verify real MCP IO."""
import argparse
import json
from pathlib import Path
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default='http://127.0.0.1:8090')
    parser.add_argument('--scenario', choices=['usage', 'document', 'all'], default='all')
    parser.add_argument('--output', default='.local/file-assistant-proof.json')
    parser.add_argument('--test-auth', action='store_true')
    parser.add_argument('--project-root', type=Path, help='Local ai-challenge-app checkout used by Project MCP')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    project = (args.project_root or root.parent / 'ai-challenge-app').resolve()
    settings = {}
    for line in (root / '.env').read_text().splitlines():
        key, sep, value = line.partition('=')
        if sep and not key.strip().startswith('#'):
            settings[key.strip()] = value.strip().strip('"\'')
    token = 'ui-validation-token-for-local-only-2026' if args.test_auth else settings['SUPPORT_ACCESS_TOKEN']
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    cases = {
        'usage': 'Найди в текущих исходниках ai-challenge-app, как работает команда /help: от входящего запроса до RAG и ответа с цитатами. Сам найди и прочитай минимум три связанных исходных файла. Объясни цепочку с путями и номерами строк. Файлы не изменяй.',
        'document': 'Изучи по текущим исходникам ai-challenge-app, как Sliding Window ограничивает контекст и что происходит с сохранённой историей. Сам найди и прочитай минимум три связанных исходных файла. Создай или обнови краткую документацию docs/demo/file-assistant-context.md с объяснением контекста и архива, хранения истории и примером поведения пользователя. Сохрани файл и покажи diff. Если документ существует, дочитай его целиком и сохраняй существующие корректные сведения; если всё уже актуально, не переписывай и не добавляй повторные разделы или дату.',
    }
    results = []
    for name, question in cases.items():
        if args.scenario not in ('all', name):
            continue
        started = time.monotonic()
        request = urllib.request.Request(args.url.rstrip('/') + '/api/chat',
                                         data=json.dumps({'question': question}, ensure_ascii=False).encode(),
                                         headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        with opener.open(request, timeout=315) as response:
            result = json.load(response)
        calls = result.get('trace', {}).get('mcpCalls', [])
        reads = {call.get('arguments', {}).get('path') for call in calls
                 if call.get('tool') == 'project_read_file' and not call.get('error')}
        writes = [call for call in calls if call.get('tool') == 'project_write_file' and not call.get('error')]
        workspace = next((call.get('result', {}) for call in calls if call.get('tool') == 'project_get_info' and not call.get('error')), {})
        checks = {'threeFilesRead': len(reads) >= 3,
                  'hasVerifiedSources': bool(result.get('sources')),
                  'correctProjectWorkspace': workspace.get('project') == 'ai-challenge-app' and workspace.get('fileWorkspace') == project.name,
                  'fileRetrieval': result.get('trace', {}).get('retrieval') == 'project_files'}
        if name == 'usage':
            checks['searchedCurrentFiles'] = any(call.get('tool') == 'project_search_files' for call in calls)
            checks['noWrites'] = not writes
        else:
            target = project / 'docs/demo/file-assistant-context.md'
            checks['documentExists'] = target.is_file() and target.stat().st_size > 0
            checks['savedOrReadExisting'] = any(call.get('result', {}).get('saved') for call in writes) or 'docs/demo/file-assistant-context.md' in reads
            checks['actualDiffShown'] = all(not call.get('result', {}).get('changed') or call.get('result', {}).get('diff', '') in result.get('answer', '') for call in writes)
        item = {'scenario': name, 'seconds': round(time.monotonic() - started, 2), 'checks': checks,
                'filesRead': sorted(reads), 'response': result}
        results.append(item)
        print(json.dumps({key: item[key] for key in ('scenario', 'seconds', 'checks', 'filesRead')}, ensure_ascii=False), flush=True)
        report = {'passed': all(all(item['checks'].values()) for item in results), 'results': results}
        output = root / args.output
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    raise SystemExit(0 if results and report['passed'] else 1)


if __name__ == '__main__':
    main()
