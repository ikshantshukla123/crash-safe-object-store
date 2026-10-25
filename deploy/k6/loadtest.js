import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  vus: Number(__ENV.K6_VUS || 10),
  duration: __ENV.K6_DURATION || '20s',
  thresholds: {
    http_req_failed: ['rate<0.05'],
    http_req_duration: ['p(95)<800'],
  },
};

const base = __ENV.BASE_URL || 'http://api:8080';

export function setup() {
  const email = `load-${Date.now()}@example.com`;
  const reg = http.post(`${base}/auth/register`, JSON.stringify({ email, password: 'load-test-password' }), {
    headers: { 'Content-Type': 'application/json' },
  });
  check(reg, { 'registered': (r) => r.status === 200 });
  const token = reg.json('token');
  const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };
  const bucket = `load-${Date.now()}`;
  const created = http.post(`${base}/buckets`, JSON.stringify({ name: bucket }), { headers });
  check(created, { 'bucket created': (r) => r.status === 201 });
  let uploaded = 0;
  for (let i = 0; i < 20; i++) {
    const put = http.put(`${base}/buckets/${bucket}/objects/seed/${i}.txt`, `load test object ${i}`, {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'text/plain' },
    });
    if (put.status === 200) uploaded++;
  }
  check(uploaded, { 'seeded 20 objects': (n) => n === 20 });
  return { token, bucket };
}

export default function (data) {
  const headers = { Authorization: `Bearer ${data.token}` };
  const list = http.get(`${base}/buckets/${data.bucket}/objects?limit=50`, { headers });
  check(list, { 'list 200': (r) => r.status === 200 });

  const key = Math.floor(Math.random() * 20);
  const get = http.get(`${base}/buckets/${data.bucket}/objects/seed/${key}.txt`, { headers });
  check(get, { 'get 200': (r) => r.status === 200, 'verified body': (r) => r.body.includes('load test object') });

  const search = http.get(`${base}/search?q=load%20test%20object&limit=5`, { headers });
  check(search, { 'search 200': (r) => r.status === 200 });
  sleep(0.2);
}
