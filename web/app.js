const money = n => new Intl.NumberFormat('ru-RU').format(n) + ' ₸';
const dateEl = document.getElementById('date');
const form = document.getElementById('trip-form');
const msg = document.getElementById('form-message');
const modal = document.getElementById('trip-modal');
const openModalBtn = document.getElementById('open-modal');
const closeModalBtn = document.getElementById('close-modal');
const cancelModalBtn = document.getElementById('cancel-modal');
let currentTraceId = '';

function makeTraceId() {
  return 'trip-' + Date.now() + '-' + Math.random().toString(16).slice(2, 8);
}

async function openModal() {
  currentTraceId = makeTraceId();
  modal.classList.add('open');
  modal.setAttribute('aria-hidden', 'false');
  setTimeout(() => {
    form.querySelector('[name="start"]').focus();
  }, 50);
}

function closeModal() {
  modal.classList.remove('open');
  modal.setAttribute('aria-hidden', 'true');
  msg.className = 'muted';
  msg.textContent = '';
  form.reset();
  currentTraceId = '';
}

openModalBtn.addEventListener('click', openModal);
closeModalBtn.addEventListener('click', closeModal);
cancelModalBtn.addEventListener('click', closeModal);
modal.addEventListener('click', e => {
  if (e.target === modal) closeModal();
});

dateEl.value = new Date().toISOString().slice(0, 10);
dateEl.addEventListener('change', load);

form.addEventListener('submit', async e => {
  e.preventDefault();
  const formData = new FormData(form);
  const startValue = formData.get('start');
  const endValue = formData.get('end');

  if (!startValue || !endValue) {
    msg.className = 'error';
    msg.textContent = 'Укажите время начала и окончания';
    return;
  }

  const payload = {
    trace_id: currentTraceId || makeTraceId(),
    start: new Date(startValue).toISOString(),
    end: new Date(endValue).toISOString(),
    amount: Number(formData.get('amount')),
    payment: String(formData.get('payment')),
    commission: Number(formData.get('commission'))
  };

  const res = await fetch('/api/trips', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  });

  const data = await res.json().catch(() => ({}));

  if (!res.ok) {
    msg.className = 'error';
    msg.textContent = data.error || 'Ошибка валидации';
    return;
  }

  if (data.duplicate) {
    msg.className = 'error';
    msg.textContent = 'Такая поездка уже существует: ' + (data.id || '');
    return;
  }

  msg.className = 'success';
  msg.textContent = 'Поездка добавлена';
  closeModal();
  dateEl.value = new Date(payload.start).toISOString().slice(0, 10);
  load();
});

async function load() {
  const d = dateEl.value;
  const [s, t] = await Promise.all([
    fetch('/api/summary?date=' + d),
    fetch('/api/trips?date=' + d)
  ]);

  if (!s.ok || !t.ok) {
    alert('Ошибка загрузки');
    return;
  }

  renderSummary(await s.json());
  renderTrips(await t.json());
}

function renderSummary(x) {
  document.getElementById('summary').innerHTML = '<h2>Сводка</h2><div class="grid">' + [
    ['Поездок', x.trips],
    ['Выручка', money(x.revenue)],
    ['Комиссия', money(x.commission)],
    ['На руки', money(x.net)],
    ['Наличные / карта', money(x.cash) + ' / ' + money(x.card)]
  ].map(a => '<div class="metric"><span>' + a[0] + '</span><b>' + a[1] + '</b></div>').join('') + '</div>';
}

function renderTrips(xs) {
  if (!xs.length) {
    document.getElementById('trips').innerHTML = '<div class="empty-state">За этот день поездок нет</div>';
    return;
  }

  document.getElementById('trips').innerHTML = '<table class="trip-table"><thead><tr><th>ID</th><th>Начало</th><th>Окончание</th><th>Сумма</th><th>Оплата</th><th>Комиссия</th><th>На руки</th></tr></thead><tbody>' + xs.map(x => '<tr data-id="' + (x.id || '') + '"><td>' + x.id + '</td><td>' + new Date(x.start).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }) + '</td><td>' + new Date(x.end).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }) + '</td><td>' + money(x.amount) + '</td><td>' + (x.payment === 'card' ? 'Карта' : 'Наличные') + '</td><td>' + money(x.commission) + '</td><td>' + money(x.amount - x.commission) + '</td></tr>').join('') + '</tbody></table>';
}

load();
