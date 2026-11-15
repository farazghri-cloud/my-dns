# راهنمای نصب HyperDNS روی اوبونتو ۲۴.۰۴

این نسخه از پروژه یک قابلیت اضافه دارد: **مسیرهای دستی (Manual Routes)** — یعنی
می‌توانید دامنه، ساب‌دامنه، وایلدکارد و میزبان SNI را خودتان اضافه کنید و DNS
علاوه بر مسیرهای پیش‌فرض، از این مسیرها هم عبور کند.

---

## ۱. پیش‌نیازها

| مورد | توضیح |
|---|---|
| سیستم‌عامل | Ubuntu 22.04 یا 24.04 (تست‌شده روی ۲۴.۰۴)، Debian 12 هم کار می‌کند |
| دسترسی | کاربر `root` (یا `sudo`) |
| دامنه | یک دامنه که رکورد **A** آن به IP سرور اشاره کند (برای پنل و گواهی HTTPS) |
| پورت‌های باز | `53/udp`، `53/tcp`، `80/tcp`، `443/tcp`، `853/tcp`، `8443/tcp` و پورت پنل (پیش‌فرض `8080/tcp`) |
| منابع | ۱ هسته / ۱ گیگ برای استفاده شخصی کافی است؛ برای فروش اشتراک ۲ هسته / ۲ گیگ توصیه می‌شود |

پورت‌ها را هم در فایروال سیستم (`ufw`) و هم در فایروال پنل ارائه‌دهندهٔ VPS باز کنید.

---

## ۲. نصب با اسکریپت (روش پیشنهادی)

اسکریپت `scripts/install-ubuntu24.sh` برنامه را **از همین سورس** کامپایل می‌کند؛
پس هر تغییری که در کد داده‌اید روی سرور هم همان است.

```bash
# روی سرور، در ریشهٔ پروژه
sudo HYPERDNS_DOMAIN=dns.example.com HYPERDNS_EMAIL=you@example.com \
     bash scripts/install-ubuntu24.sh
```

اسکریپت به‌ترتیب این کارها را انجام می‌دهد:

1. بررسی root و نسخهٔ سیستم‌عامل
2. نصب `curl`, `ca-certificates`, `tar` و در صورت نیاز Go 1.27 در `/usr/local/go`
3. توقف سرویس قبلی و آرشیو نصب موجود در `/root/hyperdns-preinstall-<تاریخ>.tar.gz`
4. ساخت `/opt/hyperdns/config.json` (فقط اگر وجود نداشته باشد) با:
   IP عمومی، پورت پنل، یوزر `admin`، پسورد و API key تصادفی، دامنه و ایمیل
5. کامپایل باینری استاتیک به `/opt/hyperdns/hyperdns`
6. آزاد کردن پورت ۵۳ از `systemd-resolved` (با پشتیبان‌گیری از `resolv.conf`)
7. ساخت یونیت systemd، فعال‌سازی و اجرای سرویس
8. نمایش آدرس پنل، یوزر/پسورد اولیه و پورت‌هایی که باید باز شوند

اگر متغیرهای `HYPERDNS_DOMAIN` و `HYPERDNS_EMAIL` را ندهید، اسکریپت در حالت
تعاملی از شما می‌پرسد. بدون دامنه هم نصب انجام می‌شود، ولی پنل فقط روی
`127.0.0.1` بالا می‌آید و باید با تونل SSH ببینید:

```bash
ssh -L 8080:127.0.0.1:8080 root@<server-ip>
```

سپس در مرورگر خودتان `http://127.0.0.1:8080/<admin-path>/dash/` را باز کنید.

> **پسورد اولیه:** در انتهای نصب یک‌بار چاپ می‌شود. همان لحظه ذخیره کنید؛
> بعد از اولین اجرا فقط هش آن در دیتابیس می‌ماند.

---

## ۳. نصب دستی (گام‌به‌گام)

اگر نمی‌خواهید اسکریپت اجرا شود:

```bash
# ۱) ابزار ساخت
apt-get update && apt-get install -y curl ca-certificates tar
curl -fsSL https://go.dev/dl/go1.27.4.linux-amd64.tar.gz -o /tmp/go.tgz
rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
export PATH=/usr/local/go/bin:$PATH

# ۲) ساخت باینری
cd /path/to/hyperdns
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /opt/hyperdns/hyperdns ./cmd/hyperdns

# ۳) کانفیگ
cp config.example.json /opt/hyperdns/config.json
nano /opt/hyperdns/config.json
#   server.public_ip         → IP عمومی سرور
#   server.admin_password    → یک رمز قوی (اجباری)
#   tls.domain / tls.email   → دامنهٔ پنل و ایمیل Let's Encrypt
#   tls.auto_cert            → true
chmod 600 /opt/hyperdns/config.json

# ۴) آزاد کردن پورت ۵۳
mkdir -p /etc/systemd/resolved.conf.d
printf '[Resolve]\nDNSStubListener=no\n' > /etc/systemd/resolved.conf.d/hyperdns.conf
chmod 644 /etc/systemd/resolved.conf.d/hyperdns.conf
systemctl restart systemd-resolved
# اگر /etc/resolv.conf به 127.0.0.53 اشاره می‌کرد، عوض کنید:
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf

# ۵) سرویس
cp scripts/hyperdns.service /etc/systemd/system/hyperdns.service
systemctl daemon-reload
systemctl enable --now hyperdns
journalctl -u hyperdns -f
```

---

## ۴. ورود به پنل

آدرس پنل در لاگ اول اجرا چاپ می‌شود:

```bash
journalctl -u hyperdns --no-pager | grep -i dash
```

مسیر پنل یک پیشوند ۱۶ کاراکتری تصادفی دارد (مثلاً
`https://dns.example.com/9f2c1a7b4d5e6f80/dash/`) و به‌صورت پیش‌فرض ساخته می‌شود.
با یوزر `admin` و پسوردی که در کانفیگ گذاشته‌اید وارد شوید.

---

## ۵. اضافه کردن مسیر دستی (قابلیت این نسخه)

مسیر: **پنل → تب Policies → انتهای بخش Gaming Policies → کارت
«Manual Routes — Domains, Subdomains & SNI»**

چند نمونه:

| ورودی | نتیجه |
|---|---|
| `example.com` | خود سایت + همهٔ ساب‌دامنه‌ها |
| `api.example.com` | همان میزبان + هر چیزی زیر آن |
| `*.example.com` | فقط ساب‌دامنه‌ها (خودِ `example.com` پوشش داده نمی‌شود) |
| `https://Example.COM/app` | خودکار به `example.com` تبدیل می‌شود |

روی **Add route** بزنید (یا Enter). هر ردیف با برچسب `DOMAIN` / `SUBDOMAIN` /
`WILDCARD` و نشان `DNS + SNI` نمایش داده می‌شود و با ضربدر حذف می‌شود. تغییر
بلافاصله اعمال می‌شود — بدون ری‌استارت و بدون پاک کردن کش دستی.

نکات:

- این مسیرها **برای همهٔ کلاینت‌ها** اعمال می‌شوند، حتی مشترکینی که سیاست
  اختصاصی خودشان را انتخاب کرده‌اند.
- مسیرهای پیش‌فرض (پریست‌ها) دست‌نخورده می‌مانند؛ مسیر دستی روی آن‌ها اضافه می‌شود.
- ورودی نامعتبر (IP، `com` تنها، وایلدکارد در وسط نام، حروف غیرانگلیسی) با پیام
  خطا رد می‌شود. برای دامنهٔ فارسی از فرم پانی‌کد (`xn--...`) استفاده کنید.
- همین لیست در تب **Settings → Custom Proxied Domains** هم دیده می‌شود؛ هر دو
  صفحه یک لیست واحد هستند.

---

## ۶. تست

از یک سیستم که DNS آن به IP سرور تنظیم شده:

```bash
dig +short cdn.example.com @<server-ip>     # باید IP سرور را برگرداند
dig +short riotgames.com @<server-ip>       # مسیر پیش‌فرض، همان IP سرور
dig +short example.org @<server-ip>         # مسیر عادی، IP واقعی سایت
```

در تب **Logs** پنل، کوئری مربوط به مسیر دستی با قانون `Custom Proxy` و اکشن
`PROXY` نمایش داده می‌شود.

روی ویندوز:

```powershell
nslookup cdn.example.com <server-ip>
```

---

## ۷. به‌روزرسانی

```bash
cd /path/to/hyperdns
git pull
sudo HYPERDNS_DOMAIN=dns.example.com bash scripts/install-ubuntu24.sh
```

`config.json`، `data.db`، `master.key` و پوشهٔ `certs/` دست‌نخورده می‌مانند و
فقط باینری جایگزین و سرویس ری‌استارت می‌شود. از نصب قبلی هم یک آرشیو در
`/root/hyperdns-preinstall-*.tar.gz` ساخته می‌شود.

---

## ۸. عیب‌یابی

| نشانه | علت و راه‌حل |
|---|---|
| `bind: address already in use` روی پورت ۵۳ | `systemd-resolved` هنوز گوش می‌دهد: `systemctl status systemd-resolved` و دراپ‌این `DNSStubListener=no` را بررسی کنید |
| پنل باز نمی‌شود | دامنه در `tls.domain` باشد و رکورد A به IP سرور اشاره کند؛ پورت ۸۰ برای صدور گواهی باید باز باشد |
| گواهی صادر نمی‌شود | `journalctl -u hyperdns \| grep -i acme` — معمولاً DNS هنوز پروپاگیت نشده یا پورت ۸۰ بسته است |
| کوئری جواب نمی‌دهد ولی سرویس بالاست | حالت دسترسی در نسخهٔ ۲.۲ به‌صورت پیش‌فرض «ویت‌لیست» است؛ IP کلاینت را در تب Clients اضافه کنید یا allow-all را روشن کنید |
| مسیر دستی اثر ندارد | همان نام را در تب Logs ببینید: اگر `DIRECT` با قانون `Unproxyable Port` یا `Realtime Media` بود، آن مسیر عمداً مستقیم می‌رود |
| پنل روی 127.0.0.1 گیر کرده | دامنه نداده‌اید؛ با `HYPERDNS_DOMAIN=...` دوباره اجرا کنید یا تونل SSH بزنید |

---

## ۹. نکات امنیتی

- `config.json` هم پسورد مدیر و هم API key را در خود دارد → `chmod 600` و بکاپ
  رمزنگاری‌شده.
- پنل را بدون دامنه و HTTPS در معرض اینترنت نگذارید.
- در نسخهٔ ۲.۲ ویت‌لیست مشتریان پیش‌فرض روشن است؛ `allow_all` را فقط وقتی
  لازم است روشن کنید.
- ترافیک مسیرهای دستی هم از سهمیه و پهنای‌باند سرور کم می‌کند؛ وایلدکارد روی
  دامنه‌های CDN مشترک را با احتیاط اضافه کنید.

---

## ۱۰. حذف نصب

```bash
sudo bash scripts/uninstall.sh
```

فایل‌های `/opt/hyperdns` (کانفیگ، دیتابیس، کلید و گواهی‌ها) قبل از حذف به‌صورت
دستی بکاپ بگیرید.
