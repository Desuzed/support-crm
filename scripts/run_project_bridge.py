"""Launch the Mac knowledge bridge using server-only local configuration."""
import argparse
import os
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--project-root', type=Path, required=True)
args = parser.parse_args()
project = args.project_root.resolve()
support = Path(__file__).resolve().parents[1]

def values(path):
    if not path.exists():
        return {}
    result = {}
    for line in path.read_text().splitlines():
        key, sep, value = line.partition('=')
        if sep and not key.strip().startswith('#'):
            result[key.strip()] = value.strip().strip('"\'')
    return result

settings = values(support / '.env')
os.environ['SUPPORT_MCP_TOKEN'] = settings['PROJECT_MCP_TOKEN']
os.environ['SUPPORT_FILES_ROOT'] = settings.get('SUPPORT_FILES_ROOT') or str(project)
project_settings = values(project / '.env')
for name in ('DEEPSEEK_API_KEY', 'GITHUB_TOKEN'):
    if name not in os.environ and project_settings.get(name):
        os.environ[name] = project_settings[name]
for name in ('SUPPORT_EMBEDDING_URL', 'SUPPORT_EMBEDDING_MODEL'):
    if name in settings:
        os.environ[name] = settings[name]
binary = project / '.local/support-project-mcp'
os.chdir(project)
os.execv(str(binary), [str(binary), '-root', str(project), '-addr', '127.0.0.1:8091'])
