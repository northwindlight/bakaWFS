#!/usr/bin/env python3
"""Deploy the Go event function, Function URL and cloud-only maintenance timer.

No Tencent credential is included in the function environment. CLI output never
contains passwords, storage keys, JWT secrets or signed object URLs.
"""
import argparse
import base64
import datetime
import hashlib
import hmac
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.request
import yaml

ROOT = Path(__file__).resolve().parents[2]

class CloudError(RuntimeError):
    pass

class Tencent:
    def __init__(self, region, ddns=None):
        self.region = region
        if ddns:
            config = yaml.safe_load(Path(ddns).read_text())
            entries = [c['dns'] for c in config['dnsconf'] if c.get('dns', {}).get('name') == 'tencentcloud']
            self.sid, self.secret = entries[0]['id'], entries[0]['secret']
        else:
            self.sid, self.secret = os.environ['TENCENTCLOUD_SECRET_ID'], os.environ['TENCENTCLOUD_SECRET_KEY']
        self.session = os.environ.get('TENCENTCLOUD_SESSION_TOKEN', '')

    def call(self, action, payload, service='scf', version='2018-04-16'):
        host = service + '.tencentcloudapi.com'
        body = json.dumps(payload, separators=(',', ':')).encode()
        now = int(time.time())
        day = datetime.datetime.fromtimestamp(now, datetime.timezone.utc).strftime('%Y-%m-%d')
        canonical = 'POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:' + host + '\n\ncontent-type;host\n' + hashlib.sha256(body).hexdigest()
        scope = f'{day}/{service}/tc3_request'
        signing = f'TC3-HMAC-SHA256\n{now}\n{scope}\n' + hashlib.sha256(canonical.encode()).hexdigest()
        def mac(key, text):
            return hmac.new(key, text.encode(), hashlib.sha256).digest()
        key = mac(mac(mac(('TC3' + self.secret).encode(), day), service), 'tc3_request')
        auth = f'TC3-HMAC-SHA256 Credential={self.sid}/{scope}, SignedHeaders=content-type;host, Signature={mac(key, signing).hex()}'
        headers = {'Content-Type': 'application/json; charset=utf-8', 'Authorization': auth, 'X-TC-Action': action, 'X-TC-Version': version, 'X-TC-Timestamp': str(now), 'X-TC-Region': self.region}
        if self.session:
            headers['X-TC-Token'] = self.session
        for attempt in range(3):
            try:
                with urllib.request.urlopen(urllib.request.Request('https://' + host, data=body, headers=headers), timeout=120) as response:
                    result = json.load(response)['Response']
                if 'Error' in result:
                    raise CloudError(action + ': ' + result['Error'].get('Code', 'unknown'))
                return result
            except (urllib.error.URLError, TimeoutError):
                if attempt == 2:
                    raise CloudError(action + ': transport outcome unknown; rerun the same deployment') from None
                time.sleep(2)

def deploy(args):
    if not args.yes:
        raise CloudError('deployment requires --yes')
    tc = Tencent(args.region, args.tencent_ddns)
    common = {'FunctionName': args.function, 'Namespace': 'default'}
    def call(action, **payload):
        return tc.call(action, dict(common, **payload))
    def wait():
        for _ in range(90):
            result = call('GetFunction', ShowCode='FALSE')
            if result.get('Status') == 'Active':
                return result
            time.sleep(1)
        raise CloudError('function activation timed out')
    keys = {}
    for line in args.keys.read_text().splitlines():
        if '=' in line and not line.startswith('#'):
            key, value = line.split('=', 1)
            keys[key.strip()] = value.strip().strip('\"\'')
    users = yaml.safe_load(args.users.read_text())
    config = yaml.safe_load(args.config.read_text())
    if not config.get('secret') or not users.get('users'):
        raise CloudError('explicit users and JWT secret are required')
    settings = {'BAKAWFS_JWT_SECRET': config['secret'], 'BAKAWFS_USERS_YAML': yaml.safe_dump({'users': users['users']}, allow_unicode=True), 'BAKAWFS_QINIU_AK': keys['QINIU_AK'], 'BAKAWFS_QINIU_SK': keys['QINIU_SK'], 'BAKAWFS_KODO_BUCKET': args.bucket, 'BAKAWFS_CATALOG_PREFIX': args.catalog_prefix, 'BAKAWFS_AUTH_MODE': 'true', 'BAKAWFS_MANGA_MODE': str(args.manga).lower(), 'BAKAWFS_LOG_ENABLED': 'false', 'GOMEMLIMIT': '384MiB'}
    if args.data_domain:
        settings['BAKAWFS_DATA_DOMAIN'] = args.data_domain.rstrip('/')
    environment = lambda: {'Variables': [{'Key': key, 'Value': str(value)} for key, value in settings.items()]}
    archive = base64.b64encode(args.zip.read_bytes()).decode()
    try:
        call('GetFunction', ShowCode='FALSE')
        exists = True
    except CloudError as error:
        if 'ResourceNotFound.Function' not in str(error):
            raise
        exists = False
    if not exists:
        call('CreateFunction', Runtime='Go1', Handler='main', Type='Event', MemorySize=512, Timeout=120, Environment=environment(), Code={'ZipFile': archive}, ClsLogsetId='', ClsTopicId='', AutoCreateClsTopic='FALSE', AutoDeployClsTopicIndex='FALSE', TraceEnable='FALSE', Description='bakaWFS cloud API: immutable Kodo catalog, direct transfers, no CLS')
        wait()
    # Disable platform collection BEFORE the first HTTP/timer invocation.
    call('UpdateFunctionConfiguration', MemorySize=512, Timeout=120, Environment=environment(), ClsLogsetId='', ClsTopicId='', IgnoreSysLog=True)
    wait()
    if exists:
        call('UpdateFunctionCode', Handler='main', ZipFile=archive, Publish='FALSE')
        wait()
    triggers = call('ListTriggers').get('Triggers', [])
    http = next((t for t in triggers if t['Type'] == 'http'), None)
    if http is None:
        http = call('CreateTrigger', Qualifier='$LATEST', TriggerName='function-url', Type='http', Enable='OPEN', TriggerDesc=json.dumps({'AuthType': 'NONE', 'NetConfig': {'EnableIntranet': False, 'EnableExtranet': True}}))['TriggerInfo']
    url = json.loads(http['TriggerDesc'])['NetConfig']['ExtranetUrl'].rstrip('/')
    settings['BAKAWFS_PUBLIC_API'] = url
    call('UpdateFunctionConfiguration', Environment=environment(), ClsLogsetId='', ClsTopicId='', IgnoreSysLog=True)
    result = wait()
    if args.timer and not any(t['TriggerName'] == 'bakawfs-maintenance' for t in triggers):
        call('CreateTrigger', Qualifier='$LATEST', TriggerName='bakawfs-maintenance', Type='timer', Enable='OPEN', TriggerDesc=json.dumps({'cron': '0 */15 * * * * *'}))
    if result.get('ClsTopicId') or result.get('ClsLogsetId') or not result.get('IgnoreSysLog'):
        raise CloudError('platform logging was not disabled')
    # Do not remove unrelated account topics. This only verifies deployment
    # never created a default chargeable topic.
    report = {'function': args.function, 'region': args.region, 'url': url, 'bucket': args.bucket, 'catalog_prefix': args.catalog_prefix, 'code_sha256': hashlib.sha256(args.zip.read_bytes()).hexdigest(), 'logs_disabled': True, 'timer_enabled': args.timer, 'deployed_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--function', default='bakawfs-api')
    parser.add_argument('--region', default='ap-guangzhou')
    parser.add_argument('--bucket', default='bakawfs-library-northwind')
    parser.add_argument('--catalog-prefix', default='catalog/v1/')
    parser.add_argument('--data-domain', default='')
    parser.add_argument('--zip', type=Path, default=ROOT/'dist/scf/bakawfs-scf.zip')
    parser.add_argument('--users', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--keys', type=Path, default=Path('/etc/qiniu/keys.env'))
    parser.add_argument('--tencent-ddns', type=Path)
    parser.add_argument('--manga', action=argparse.BooleanOptionalAction, default=False)
    parser.add_argument('--timer', action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument('--output', type=Path, default=ROOT/'dist/scf/deployment.json')
    parser.add_argument('--yes', action='store_true')
    args = parser.parse_args()
    try:
        deploy(args)
    except (CloudError, KeyError, ValueError, OSError):
        # Configuration parsers and transport errors can quote secrets or URLs.
        # CloudError messages are generated here and contain action/code only.
        import sys
        error = sys.exc_info()[1]
        raise SystemExit(str(error) if isinstance(error, CloudError) else 'deployment input or transport failed') from None
