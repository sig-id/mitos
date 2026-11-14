(() => {
 'use strict';
 const minuteIntervals = [1, 2, 5, 10, 15, 20, 30];
 const hourIntervals = [1, 2, 3, 4, 6, 8, 12];
 const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
 function schedule({frequency, minutes, hours, time, weekday, cron}) {
  if (frequency === 'custom') {
   if (!cron.trim()) throw new Error('Enter a cron expression.');
   return {cron: cron.trim(), description: 'Runs on your custom cron schedule.'};
  }
  if (frequency === 'minutes' && minuteIntervals.includes(Number(minutes))) {
   const n = Number(minutes);
   return {cron: n === 1 ? '* * * * *' : `*/${n} * * * *`, description: `Every ${n === 1 ? 'minute' : n + ' minutes'}, aligned to the hour.`};
  }
  if (frequency === 'hours' && hourIntervals.includes(Number(hours))) {
   const n = Number(hours);
   return {cron: n === 1 ? '0 * * * *' : `0 */${n} * * *`, description: n === 1 ? 'Every hour, on the hour.' : `Every ${n} hours, on the hour, starting at midnight.`};
  }
  if ((frequency === 'daily' || frequency === 'weekly') && /^([01]\d|2[0-3]):[0-5]\d$/.test(time)) {
   const [hour, minute] = time.split(':').map(Number);
   if (frequency === 'daily') return {cron: `${minute} ${hour} * * *`, description: `Every day at ${time}.`};
   if (/^[0-6]$/.test(String(weekday))) return {cron: `${minute} ${hour} * * ${weekday}`, description: `Every ${days[weekday]} at ${time}.`};
  }
  throw new Error('Choose a valid schedule and time.');
 }
 function init(form) {
  const $ = id => document.getElementById(id);
  const zone = form.elements.timezone;
  zone.value = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const now = new Date();
  $('graph-schedule-time').value = `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`;
  $('graph-weekday').value = String(now.getDay());
  function value() {
   return schedule({frequency: $('graph-frequency').value, minutes: $('graph-minute-interval').value, hours: $('graph-hour-interval').value, time: $('graph-schedule-time').value, weekday: $('graph-weekday').value, cron: form.elements.cron.value});
  }
  function show(id, visible) {
   const field = $(id);
   field.hidden = !visible;
   field.querySelectorAll('input,select').forEach(input => { input.disabled = !visible; });
  }
  function update() {
   const mode = form.elements.trigger.value, frequency = $('graph-frequency').value;
   const recurring = mode !== 'once', timed = mode === 'cron' || mode === 'both';
   show('graph-recurring-fields', recurring);
   show('graph-cron-fields', timed);
   show('graph-event-fields', recurring && mode !== 'cron');
   show('graph-schedule-advanced', timed);
   show('graph-timezone-field', timed);
   show('graph-minute-field', timed && frequency === 'minutes');
   show('graph-hour-field', timed && frequency === 'hours');
   show('graph-time-field', timed && ['daily', 'weekly'].includes(frequency));
   show('graph-weekday-field', timed && frequency === 'weekly');
   show('graph-custom-field', timed && frequency === 'custom');
   $('graph-trigger-summary').textContent = form.elements.trigger.selectedOptions[0].textContent;
   $('graph-create').textContent = recurring ? 'Save workflow →' : 'Start work →';
   zone.setCustomValidity('');
   if (!timed) return;
   try {
    new Intl.DateTimeFormat(undefined, {timeZone: zone.value.trim()});
   } catch {
    zone.setCustomValidity('Enter a time zone such as Europe/Zurich or America/New_York.');
   }
   try {
    $('graph-schedule-summary').textContent = `${value().description} Time zone: ${zone.value.trim() || 'choose a time zone'}. First run at the next matching time.`;
   } catch (error) {
    $('graph-schedule-summary').textContent = error.message;
   }
  }
  form.addEventListener('change', update);
  zone.addEventListener('input', update);
  form.elements.cron.addEventListener('input', update);
  form.addEventListener('invalid', event => {
   for (let parent = event.target.parentElement; parent && parent !== form; parent = parent.parentElement) {
    if (parent.tagName === 'DETAILS') parent.open = true;
   }
  }, true);
  update();
  return {value};
 }
 globalThis.MitosWorkSchedule = {schedule, init};
})();
