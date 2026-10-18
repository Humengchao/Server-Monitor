import i18n from './index';
const en = {
  network: 'Network monitoring', networkHint: 'One ICMP sample per minute. Packet loss is measured from sent and received packets; it is independent of SSH health.',
  add: 'Add check', edit: 'Edit check', name: 'Name', source: 'Probe source', target: 'Target server', local: 'Panel host', packets: 'Packets (1–10)',
  lossThreshold: 'Packet loss threshold (%)', latencyThreshold: 'Average RTT threshold (ms, optional)', enabled: 'Enabled', save: 'Save', cancel: 'Cancel', remove: 'Delete', confirm: 'Delete this check and its history?',
  run: 'Probe now', refresh: 'Refresh', failed: 'Request failed', empty: 'No checks yet', status: 'ICMP result', sent: 'Sent / received', loss: 'Packet loss', avg: 'Average RTT', minmax: 'Min / max RTT', mdev: 'RTT deviation', time: 'Time',
  compare: 'Compare probe sources', history: 'History', hour: 'Last hour', day: 'Last 24 hours', week: 'Last 7 days', historyHint: 'Loss totals are weighted by packet counts. Unknown attempts are excluded from the loss percentage.',
  unknown: 'Unknown', known: 'Measured samples', unknownCount: 'Unknown / failed attempts', partialHistory: 'Only the most recent 2,000 samples per check are plotted; summary covers the full selected interval.',
  ok: 'Replies received', down: 'No ICMP replies', error: 'Probe failed', lossState: 'Packet loss', threshold: 'Threshold exceeded', paused: 'Paused', source_unreachable: 'Probe source unavailable', probe_failed: 'Ping failed or unavailable', timeout: 'Read deadline reached', invalid_target: 'Unsupported target address', unrecognized_output: 'Probe output not recognized', output_limit: 'Probe output exceeded limit',
  logs: 'Logs', logsHint: 'Read-only bounded queries. Linux system logs require journald access; container output includes stdout and stderr. Follow uses short, cancellable cursor requests.',
  server: 'Server', container: 'Container ID (optional)', since: 'From', until: 'Until (empty for current time)', unit: 'Service unit (optional)', priority: 'Maximum priority', keyword: 'Literal, case-sensitive text', apply: 'Apply filters', next: 'Read next page', follow: 'Follow new entries', stop: 'Pause follow', export: 'Export visible logs',
  rows: 'Rows', scanned: 'Scanned', stream: 'Stream', message: 'Message', journal: 'System journal', all: 'All levels', noLogs: 'No matching entries in this scanned page', noServer: 'Select a server',
  truncated: 'The read reached its row or byte limit. More entries may exist; use the next cursor or narrow the time range.',
  access_denied: 'Journal or Docker access is restricted. Entries may be incomplete; check the configured account permissions.',
  unsupported: 'This host does not provide a supported system journal.', command_failed: 'The remote command failed; any returned entries may be incomplete.',
  cursor_unavailable: 'The journal cursor is no longer available (for example, after rotation). Refresh with a new time range.',
  invalid_record: 'Some journal records could not be decoded.', byte_limit: 'The byte limit was reached; narrow the range to inspect omitted data.',
  tail_limit: 'Docker returned only the latest bounded tail for this interval. Older lines are omitted; narrow the interval.', unparsed_output: 'Some remote output lacked Docker timestamps and could not be interpreted as a log entry.',
  bufferLimit: 'Only the latest 2,000 rows are retained in the browser.', openLogs: 'Inspect logs', networkLink: 'Network checks', chooseChecks: 'Select up to four checks',
};
const zh: typeof en = {
  network: '网络监测', networkHint: '每分钟独立采样一次 ICMP。丢包率来自实际发出和收到的包数，与 SSH 在线状态相互独立。',
  add: '添加检查', edit: '编辑检查', name: '名称', source: '探测来源', target: '目标服务器', local: '面板主机', packets: '探测包数（1–10）',
  lossThreshold: '丢包阈值（%）', latencyThreshold: '平均 RTT 阈值（毫秒，可选）', enabled: '启用', save: '保存', cancel: '取消', remove: '删除', confirm: '删除此检查及其历史数据？',
  run: '立即探测', refresh: '刷新', failed: '请求失败', empty: '暂无网络检查', status: 'ICMP 结果', sent: '发出 / 收到', loss: '丢包率', avg: '平均 RTT', minmax: '最小 / 最大 RTT', mdev: 'RTT 标准差', time: '时间',
  compare: '多探测点对比', history: '历史记录', hour: '最近一小时', day: '最近 24 小时', week: '最近 7 天', historyHint: '汇总丢包率按包数加权。未知或失败的探测不计为丢包。',
  unknown: '未知', known: '有效样本数', unknownCount: '未知 / 失败次数', partialHistory: '每个检查仅绘制最近 2,000 个样本，汇总仍覆盖整个选择区间。',
  ok: '收到回复', down: '未收到 ICMP 回复', error: '探测失败', lossState: '存在丢包', threshold: '超过阈值', paused: '已暂停', source_unreachable: '探测来源不可用', probe_failed: 'Ping 不可用或执行失败', timeout: '读取超时', invalid_target: '不支持的目标地址', unrecognized_output: '无法识别探测输出', output_limit: '探测输出超限',
  logs: '日志排障', logsHint: '只读、有界查询。Linux 系统日志需要 journald 读取权限；容器输出保留 stdout 和 stderr。实时跟随采用可取消的游标短轮询。',
  server: '服务器', container: '容器 ID（可选）', since: '开始时间', until: '结束时间（留空为当前时间）', unit: '服务单元（可选）', priority: '最高日志级别', keyword: '字面关键词（区分大小写）', apply: '应用筛选', next: '读取下一页', follow: '实时跟随', stop: '暂停跟随', export: '导出当前日志',
  rows: '条数', scanned: '已扫描', stream: '来源', message: '内容', journal: '系统日志', all: '全部级别', noLogs: '当前扫描页无匹配日志', noServer: '请选择服务器',
  truncated: '已达到行数或字节上限，可能还有日志。请继续读取游标或缩小时间范围。',
  access_denied: 'Journal 或 Docker 访问权限不足，当前内容可能不完整，请检查配置账户权限。', unsupported: '此主机不提供受支持的系统日志。', command_failed: '远端命令失败，已返回内容可能不完整。',
  cursor_unavailable: '日志游标已失效（可能发生轮转），请刷新并选择新的时间范围。', invalid_record: '部分日志记录无法解析。', byte_limit: '达到字节上限，请缩小时间范围检查遗漏内容。',
  tail_limit: 'Docker 仅返回此区间末尾的有限条日志，较早内容已省略，请缩小时间范围。', unparsed_output: '部分输出不含 Docker 时间戳，无法作为日志记录解析。',
  bufferLimit: '浏览器仅保留最近 2,000 条日志。', openLogs: '查看相关日志', networkLink: '网络检查', chooseChecks: '最多选择四个检查',
};
i18n.addResourceBundle('en', 'networkLogs', en, true, true);
i18n.addResourceBundle('zh', 'networkLogs', zh, true, true);
