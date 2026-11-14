const {test} = require('node:test');
const assert = require('node:assert/strict');
require('../static/work-schedule.js');
const {schedule} = globalThis.MitosWorkSchedule;

test('clock-aligned intervals have uniform spacing across hour/day boundaries', () => {
 for (const n of [1, 2, 5, 10, 15, 20, 30]) {
  const result = schedule({frequency: 'minutes', minutes: String(n)});
  assert.equal(result.cron, n === 1 ? '* * * * *' : `*/${n} * * * *`);
  assert.equal(60 % n, 0);
 }
 for (const n of [1, 2, 3, 4, 6, 8, 12]) {
  assert.equal(schedule({frequency: 'hours', hours: n}).cron, n === 1 ? '0 * * * *' : `0 */${n} * * *`);
  assert.equal(24 % n, 0);
 }
 // A cron step of seven resets on each hour/day, so it cannot promise every seven.
 assert.throws(() => schedule({frequency: 'minutes', minutes: 7}));
 assert.throws(() => schedule({frequency: 'hours', hours: 7}));
});
test('daily and weekly wall-clock schedules preserve midnight and Sunday', () => {
 assert.equal(schedule({frequency: 'daily', time: '00:00'}).cron, '0 0 * * *');
 assert.equal(schedule({frequency: 'daily', time: '23:59'}).cron, '59 23 * * *');
 assert.equal(schedule({frequency: 'weekly', time: '09:05', weekday: '0'}).cron, '5 9 * * 0');
 assert.match(schedule({frequency: 'weekly', time: '09:05', weekday: '1'}).description, /Monday/);
 for (const time of ['', '24:00', '09:60']) assert.throws(() => schedule({frequency: 'daily', time}));
 assert.throws(() => schedule({frequency: 'weekly', time: '09:00', weekday: '7'}));
});
test('custom expressions are preserved for server validation', () => {
 assert.equal(schedule({frequency: 'custom', cron: ' 0 9 * * 1-5 '}).cron, '0 9 * * 1-5');
 assert.throws(() => schedule({frequency: 'custom', cron: ' '}));
});
