#!/usr/bin/env node
// 用量图积分序列的行为校验（由 TestUsageChartCreditSeries 调用）。
//
// 只把 renderUsageChart 及其依赖从 app.js 里切出来执行——整份 app.js 顶层有大量
// DOM 事件绑定，在 Node 里跑不起来（也不需要跑）。
//
// 用法: node chart_credit_check.js <app.js 路径>
'use strict';
const fs = require('fs');

const appPath = process.argv[2];
if (!appPath) { console.error('用法: node chart_credit_check.js <app.js>'); process.exit(2); }
const src = fs.readFileSync(appPath, 'utf8');

// extractFunc 按函数名切出完整定义（靠花括号配平，而不是找下一个 function）。
function extractFunc(text, name) {
  const start = text.indexOf('function ' + name + '(');
  if (start < 0) throw new Error('未找到函数 ' + name);
  let depth = 0;
  for (let j = text.indexOf('{', start); j < text.length; j++) {
    if (text[j] === '{') depth++;
    else if (text[j] === '}') { depth--; if (depth === 0) return text.slice(start, j + 1); }
  }
  throw new Error('括号不平衡: ' + name);
}

// 依赖闭包从 renderUsageChart 手工推导（只这 6 个 + trimFixed，被 fmtCredit 调用）。
// 比整份 app.js 引入面小得多，也让 Node 里不需要任何 DOM 环境就能跑。
const NEEDED = ['fmtTok', 'fmtCredit', 'esc', 'parsePointTime', 'fmtTokTimeLabel',
                'renderUsageChart', 'trimFixed'];
const helpers = NEEDED.map(n => extractFunc(src, n)).join('\n');

// 最小 DOM 桩：renderUsageChart 只写 $('usChart').innerHTML 与 $('usChartNote').textContent。
const nodes = {};
const $ = id => (nodes[id] = nodes[id] || { innerHTML: '', textContent: '',
                                            addEventListener() {}, set onchange(_) {} });
global.$ = $;
const usageData = {};

const harness = 'const usageData = {};\n' + helpers + '\nreturn renderUsageChart;';
const renderUsageChart = new Function(harness)();

// 三种形态各跑一次，数 SVG 元素。
function run(series) {
  const chart = $('usChart');
  chart.innerHTML = '';
  renderUsageChart(series);
  const svg = chart.innerHTML;
  return {
    circles: (svg.match(/<circle/g) || []).length,
    polylines: (svg.match(/<polyline/g) || []).length,
    hasCreditAxis: svg.includes('>积分</text>'),
  };
}

const pt = (hour, credits, samples, tokens) => ({
  t: '2026-10-04T' + hour, scope: 'hour',
  prompt_tokens: 100, completion_tokens: 50, total_tokens: 150, requests: 1,
  credits, credit_samples: samples, credit_tokens: tokens,
});

const fails = [];
function check(name, got, want) {
  for (const k of Object.keys(want)) {
    if (got[k] !== want[k]) fails.push(`${name}: ${k}=${got[k]} want ${want[k]}`);
  }
}

// 形态 1：全 0 观测 —— 必须有点、有积分轴（修复前的缺陷：整条轴消失）。
check('all-zero', run([pt('10', 0, 1, 200)]),
      { circles: 1, hasCreditAxis: true });

// 形态 2：混合 —— 两点 + 一条折线。
check('mixed', run([pt('10', 0, 1, 200), pt('11', 2.5, 1, 500)]),
      { circles: 2, polylines: 1, hasCreditAxis: true });

// 形态 3：无观测 —— 不画积分轴，也不画点。
check('no-observation', run([pt('10', 0, 0, 0)]),
      { circles: 0, polylines: 0, hasCreditAxis: false });

if (fails.length) {
  console.error('用量图积分序列渲染不符合契约:');
  for (const f of fails) console.error('  ' + f);
  process.exit(1);
}
console.log('✓ 用量图积分序列三种形态均符合契约');
