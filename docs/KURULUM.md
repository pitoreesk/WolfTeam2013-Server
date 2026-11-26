# WolfTeam 2013 / 2012 TR Sunucu Kurulum ve İşletim Rehberi

Bu rehber, sunucuyu hem **Docker Compose** ile hem de **Docker olmadan doğrudan Windows Server / VDS** üzerinde sıfırdan kurup 2013 TR veya Eylül 2012 TR istemcisiyle oyuna bağlanmanız için gereken tüm adımları içerir.

---

## İçindekiler

1. [Gereksinimler ve Mimari](#1-gereksinimler-ve-mimari)
2. [Yöntem A: Docker Compose ile Kurulum](#2-yöntem-a-docker-compose-ile-kurulum)
3. [Yöntem B: Native Windows Server / VDS Kurulumu](#3-yöntem-b-native-windows-server--vds-kurulumu)
4. [İstemci Modları: 2013 TR vs Eylül 2012 TR](#4-i̇stemci-modları-2013-tr-vs-eylül-2012-tr)
5. [Başlatıcı (Launcher) Kurulumu](#5-başlatıcı-launcher-kurulumu)
6. [Hesap Yönetimi, GM Yetkisi ve Güvenlik](#6-hesap-yönetimi-gm-yetkisi-ve-güvenlik)
7. [Ortam Değişkenleri Referansı](#7-ortam-değişkenleri-referansı)
8. [Sık Karşılaşılan Hatalar ve Çözümleri](#8-sık-karşılaşılan-hatalar-ve-çözümleri)

---

## 1. Gereksinimler ve Mimari

### Yazılım Gereksinimleri

- **Docker yöntemi için:** Docker Engine + Docker Compose v2 (`docker compose`)
- **Native Windows / Linux yöntemi için:**
  - Go `1.25+`
  - PostgreSQL `17` (`pgcrypto` eklentisi dahil — standart EnterpriseDB paketinde gelir)
- **Windows C# başlatıcıyı derlemek için:** Ekstra IDE gerekmez; Windows'un yerleşik `.NET Framework 4.x` (`csc.exe`) derleyicisi yeterlidir.

### Ağ Portları (Firewall & Cloud Security List)

VDS (Oracle Cloud, AWS, Hetzner vb.) kullanıyorsanız aşağıdaki portları hem **işletim sistemi güvenlik duvarında** hem de **bulut sağlayıcı VCN / Security Group** panelinde açmalısınız:

| Port | Protokol | Servis | Zorunluluk |
|---|---|---|---|
| `40706` | TCP | Broker (Sunucu/Kanal Listesi) | **Zorunlu** |
| `40707` | TCP & UDP | Ana Oyun Sunucusu + UDP Bootstrap | **Zorunlu** |
| `40708` | UDP | Bot Peer Bootstrap (`GamePort + 1`) | **Zorunlu** (Botlu maçlar) |
| `40709` | TCP | Arkadaşlık (Buddy) | Önerilen |
| `40710` | UDP | Genel Relay UDP | Önerilen |
| `41000-41099` | UDP | Oda Bazlı Relay UDP (`PEER_ENDPOINT_MODE=2`) | **Zorunlu** (Çok oyunculu NAT geçişi) |
| `40719` | TCP | Oyun İçi Sıralama (Ranking Web) | İsteğe bağlı |
| `8080` | TCP | HTTP Patcher / `/register` / `/healthz` | Önerilen |
| `8444` | TCP | Launcher TLS Auth | İsteğe bağlı |

> **Önemli:** PostgreSQL (`5432`) portunu **asla** dış internete açmayın; sadece `127.0.0.1` üzerinde dinleyin.

---

## 2. Yöntem A: Docker Compose ile Kurulum

### Adım 1: `.env` dosyasını hazırlayın

```bash
cp docker/.env.example docker/.env
```

`docker/.env` dosyasını açıp düzenleyin (örnek VDS IP'si olarak `203.0.113.10` gösterilmiştir):

```dotenv
DB_USER=wolfteam
DB_PASSWORD=GucluBirVeritabaniSifresi_2026!
ADVERTISED_IP=203.0.113.10
PUBLIC_IP=203.0.113.10

# 2013 TR istemcisi için:
WT_CLIENT_2012=false
WT_WPACK_ENABLED=true

# Eylül 2012 TR istemcisi için bunun yerine:
# WT_CLIENT_2012=true
# WT_WPACK_ENABLED=false
```

> Eğer makinede 80 portu başka bir web sunucusu (IIS, Nginx, Apache) tarafından kullanılıyorsa `HTTP_PORT=8081` şeklinde çakışmayan bir port tanımlayın.

### Adım 2: Yığını derleyip başlatın

```bash
cd docker
docker compose up -d --build
```

Docker sırasıyla:
1. `postgres` konteynerini başlatır ve `docker/postgres.sql` temel şemasını yükler.
2. `migrate` konteyneri `docker/migrations/002..048` dosyalarının tamamını sırayla uygular.
3. `game` (`40707`), `ladder` (`40711`) ve `pride` (`40715`) Go sunucularını başlatır.

### Adım 3: Durumu doğrulayın

```bash
docker compose ps
curl http://127.0.0.1:8080/healthz
```

`200 OK` yanıtı gördüğünüzde sunucu hazırdır.

---

## 3. Yöntem B: Native Windows Server / VDS Kurulumu

Windows Server (ör. Oracle Cloud Windows Server 2022/2025) üzerinde Docker olmadan ve mevcut IIS/web sitelerine dokunmadan kurulum adımları:

### Adım 1: PostgreSQL 17'yi Windows Servisi Olarak Kurun

> **Kritik Not:** PostgreSQL'i SSH oturumu içinde `pg_ctl start` ile doğrudan başlatmayın; Windows OpenSSH bağlantıyı kapattığında çocuk süreçleri sonlandırabilir. Mutlaka **Windows Service** olarak kaydedin.

1. EnterpriseDB PostgreSQL 17 Windows x64 zip paketini `D:\WolfTeam\pgsql` klasörüne açın.
2. Veri ve log klasörlerini oluşturup `NT AUTHORITY\NetworkService` hesabına yetki verin:
   ```powershell
   New-Item -ItemType Directory -Force -Path D:\WolfTeam\pgdata, D:\WolfTeam\logs | Out-Null
   icacls D:\WolfTeam\pgdata /grant "NT AUTHORITY\NetworkService:(OI)(CI)F" /T
   icacls D:\WolfTeam\logs   /grant "NT AUTHORITY\NetworkService:(OI)(CI)F" /T
   ```
3. Veritabanı kümesini başlatın (`initdb`):
   ```powershell
   $pwFile = "$env:TEMP\pg_pw.txt"
   Set-Content -Path $pwFile -Value "GUCLU_SUPERUSER_SIFRESI" -NoNewline -Encoding Ascii
   & D:\WolfTeam\pgsql\bin\initdb.exe -D D:\WolfTeam\pgdata -U postgres --encoding=UTF8 --locale=C --pwfile=$pwFile
   Remove-Item -Force $pwFile
   ```
4. `D:\WolfTeam\pgdata\postgresql.conf` içinde yalnızca yerel erişim açık olsun:
   ```ini
   listen_addresses = '127.0.0.1'
   port = 5432
   ```
5. Windows Servisi olarak kaydedip başlatın:
   ```powershell
   & D:\WolfTeam\pgsql\bin\pg_ctl.exe register -N "WolfTeam-Postgres17" -D "D:\WolfTeam\pgdata" -U "NT AUTHORITY\NetworkService"
   Start-Service "WolfTeam-Postgres17"
   ```

### Adım 2: Veritabanını ve Tüm Migrasyonları Yükleyin

```powershell
$env:PGPASSWORD = "GUCLU_SUPERUSER_SIFRESI"
$psql = "D:\WolfTeam\pgsql\bin\psql.exe"

& $psql -h 127.0.0.1 -p 5432 -U postgres -c "CREATE ROLE wolfteam LOGIN PASSWORD 'GUCLU_DB_SIFRESI';"
& $psql -h 127.0.0.1 -p 5432 -U postgres -c "CREATE DATABASE wolfteam OWNER wolfteam;"
& $psql -h 127.0.0.1 -p 5432 -U postgres -d wolfteam -c "CREATE EXTENSION IF NOT EXISTS pgcrypto;"

# Temel şema + 48 migrasyon
$env:PGPASSWORD = "GUCLU_DB_SIFRESI"
& $psql -h 127.0.0.1 -p 5432 -U wolfteam -d wolfteam -v ON_ERROR_STOP=1 -f "docker\postgres.sql"
Get-ChildItem "docker\migrations\*.sql" | Sort-Object Name | ForEach-Object {
    & $psql -h 127.0.0.1 -p 5432 -U wolfteam -d wolfteam -v ON_ERROR_STOP=1 -f $_.FullName
}
```

#### Tek Süreç Çalıştırıyorsanız (Sadece Port `40707`)

Migrasyonlar `40711` (Ladder) ve `40715` (Pride Battle) kanallarını da ekler. Eğer yalnızca tek bir `wtserver.exe` (`40707`) çalıştıracaksanız, oyuncuların kapalı portlu kanallara tıklayıp kopmaması için onları devre dışı bırakın:

```powershell
& $psql -h 127.0.0.1 -p 5432 -U wolfteam -d wolfteam -c "UPDATE channels SET enabled = false WHERE port <> 40707;"
```

### Adım 3: Go Sunucusunu Derleyin

```powershell
cd go-gameserver
go build -trimpath -ldflags="-s -w" -o D:\WolfTeam\wolf2013\wtserver.exe ./cmd/wtserver
go build -trimpath -ldflags="-s -w" -o D:\WolfTeam\wolf2013\wtadmin.exe  ./cmd/wtadmin
```

Gerekli veri dosyalarını `D:\WolfTeam\wolf2013\` klasörüne kopyalayın:
- `go-gameserver\shopdata.dat`
- `go-gameserver\setitemdata.dat`
- `go-gameserver\bighitboxitem.dat`
- `go-gameserver\bf_schedule.bin`
- `notice\` klasörü -> `D:\WolfTeam\wolf2013\notice\`

### Adım 4: Başlatma Betiği ve Windows Zamanlanmış Görevi

[`docs/start-windows.example.ps1`](start-windows.example.ps1) şablonunu `D:\WolfTeam\wolf2013\start.ps1` olarak kopyalayıp kendi IP ve DB şifrenize göre düzenleyin.

Ardından sunucuyu sistem açılışında otomatik başlayacak bir Windows Zamanlanmış Görevi (`SYSTEM` veya yönetici hesabıyla) olarak kaydedin:

```powershell
$action  = New-ScheduledTaskAction -Execute "powershell.exe" -Argument "-NoProfile -ExecutionPolicy Bypass -File D:\WolfTeam\wolf2013\start.ps1" -WorkingDirectory "D:\WolfTeam\wolf2013"
$trigger = New-ScheduledTaskTrigger -AtStartup
Register-ScheduledTask -TaskName "WolfTeam-Go-Server" -Action $action -Trigger $trigger -User "SYSTEM" -RunLevel Highest -Force
Start-ScheduledTask -TaskName "WolfTeam-Go-Server"
```

### Adım 5: Windows Güvenlik Duvarı Kuralları

```powershell
New-NetFirewallRule -DisplayName "WolfTeam TCP" -Direction Inbound -Protocol TCP -LocalPort 8080,8444,40706,40707,40709,40719 -Action Allow
New-NetFirewallRule -DisplayName "WolfTeam UDP" -Direction Inbound -Protocol UDP -LocalPort 40707,40708,40710,41000-41099 -Action Allow
```

Bulut VDS kullanıyorsanız aynı portları bulut sağlayıcınızın **VCN / Security List / Firewall** panelinden de açmayı unutmayın.

---

## 4. İstemci Modları: 2013 TR vs Eylül 2012 TR

Sunucu varsayılan olarak **2013 TR** protokolünü konuşur (`WT_CLIENT_2012=false`). Eğer **Eylül 2012 TR** istemcisi kullanıyorsanız `WT_CLIENT_2012=true` (ve `WT_WPACK_ENABLED=false`) ayarlamanız gerekir.

### `WT_CLIENT_2012=true` ile Uyumlu Hale Getirilen Paketler

| Paket / İşlem | Opcode | 2013 TR Düzeni | 2012 TR Düzeni (`WT_CLIENT_2012=true`) |
|---|---|---|---|
| Oda Kurma / Kural Değiştirme İsteği | `0x160C` / `0x1614` | 16 sabit bayt (`WeaponFlags` var) | 15 sabit bayt (`WeaponFlags` yok) |
| Oda Kurma Yanıtı | `0x160D` | `U32(0)` + `U16(room)` | Doğrudan `U16(room)` |
| Oda Listesi | `0x1601` | `00 00 00 <count>` + `WeaponFlags` | `00 <count>`, `WeaponFlags` yok |
| Oda Durumu / Kural Yanıtı | `0x1603` / `0x1615` | `WeaponFlags` baytı dahil | `WeaponFlags` baytı yok |
| Oda Oyuncu Listesi (Roster) | `0x1605` | `WeaponFlags` + üye başına `Slot` sonrası `U32(0)` | `WeaponFlags` yok, `Slot` sonrası `U32(0)` yok |
| Odaya Giriş Yanıtı | `0x1607` | 6 bayt boşluk + `Slot` + 4 bayt boşluk | `U16(0)` + `Slot` |
| Kullanıcı Bilgisi | `0x1335` | 12 × `U32` + `U8` P.T. bonus | 13 × `U32` (P.T. bonus yok) |
| Maç Sonu / Oyun Puanı | `0x161B` / `0x1633` | 4 × `U32` + `WeaponFlags` / P.T. bonus | 6 × `U32`, `WeaponFlags` ve P.T. bonus yok |
| Kanal Sohbeti | `0x130E` / `0x130F` | `Color (U32)` + `Prefix` + `Text` | Yalnızca `Type` + `Text` |
| Eşya Satın Alma / Kullanma | `0x1405` / `0x141B` | `U16(0)` + `WC` / GP-seviye bloğu | `WC` ve ekstra GP-seviye alanları yok |
| Etkinlik Dükkanı | `0x1357` | 3 baytlık Forge girdisi + `premium` baytı | 2 baytlık Forge girdisi, `premium` yok |

---

## 5. Başlatıcı (Launcher) Kurulumu

### Seçenek 1: 2013 TR İstemcisi — Python Başlatıcı (`client-launcher/`)

1. `client-launcher/` içindeki dosyaları (`wt_launcher.py`, `launcher_settings.json`, `baslat_launcher.bat`) oyun klasörüne (veya yanına) kopyalayın.
2. `launcher_settings.json` içindeki `server_url` değerini sunucunuzun HTTP adresine ayarlayın (ör. `http://203.0.113.10:8080`).
3. `Img\wt_interface\attributes\server\server.txt` dosyasında sunucu IP'sinin kendi sunucu IP'niz olduğundan emin olun.
4. `baslat_launcher.bat` ile çalıştırın; **YENİ HESAP OLUŞTUR** butonu üzerinden `/register` uç noktasıyla doğrudan kayıt olabilirsiniz.

### Seçenek 2: Eylül 2012 TR (veya 2013 TR) — Windows C# Başlatıcı (`launcher/`)

2012 TR istemcisinde iki özel engel bulunur:
1. Eski `NyxLauncher` / Avital (`webd.xoe`), artık kapanmış Joygame CDN'inden `update.u` dosyasını HTTP `Range` (`206 Partial Content`) ile çekmeye çalışır.
2. İstemci içine gömülü broker adresi (`212.68.41.11:30706`) değiştirilemez.

`launcher/WolfTeamLauncher.cs` her iki sorunu istemci dosyalarını değiştirmeden çözer:

1. `launcher\build.bat` betiğini çalıştırarak `WolfTeamLauncher.exe` dosyasını derleyin.
2. `WolfTeamLauncher.exe` uygulamasını **Yönetici olarak** başlatın.
3. **Oyun Klasörü** olarak `Wolfteam.bin` dosyasının bulunduğu klasörü, **Sunucu IP** olarak kendi sunucu IP'nizi, **Port** olarak `40706` girin.
4. **Başlatma Modu:** `Wolfteam.bin (Önerilen - Patch Bypass)` seçili kalsın.
5. **Avital / update.u Bypass** ve (2012 istemci için) **2012 Broker Yönlendirme** kutucuklarını işaretli bırakın.
   - Yönlendirme **yalnızca** Windows `Loopback Pseudo-Interface 1` (`InterfaceIndex 1`, `-SkipAsSource $true`) üzerine eklenir; Ethernet/Wi-Fi DHCP ayarlarınıza dokunmaz ve uygulama kapanınca temizlenir.
6. **Sunucu Kontrol** ile `40706` ve `40707` erişimini doğrulayıp **OYUNU BAŞLAT**'a basın.

> **Önemli:** Oyun açıkken `WolfTeamLauncher.exe` penceresini **kapatmayın** (Avital HTTP sunucusu ve Broker Relay bu uygulama açık olduğu sürece çalışır).

---

## 6. Hesap Yönetimi, GM Yetkisi ve Güvenlik

### Hesap Oluşturma

- **Web / Launcher Kayıt:** Sunucu açıksa `http://<SUNUCU_IP>:8080/register` adresinden tarayıcıyla veya `wt_launcher.py` üzerinden hesap açabilirsiniz.
- **Komut Satırı (`wtadmin`):**
  ```powershell
  $env:WT_DATABASE_URL = "postgres://wolfteam:GUCLU_DB_SIFRESI@127.0.0.1:5432/wolfteam?sslmode=disable"
  $env:WT_ACCOUNT_PASSWORD = "YeniKullaniciSifresi123"
  .\wtadmin.exe create oyuncu1
  ```
- **Şifre Değiştirme:**
  ```powershell
  $env:WT_ACCOUNT_PASSWORD = "YeniGucluAdminSifresi!"
  .\wtadmin.exe password admin
  ```

### GM (Oyun Yöneticisi) Yetkisi Verme

`users.authority` sütunu oyun içi GM yetkisini belirler (`12` = GM):

```sql
UPDATE users SET authority = 12 WHERE username = 'oyuncu1';
```

### Canlıya Çıkmadan Önce Güvenlik Kontrol Listesi

- [ ] `admin` hesabının varsayılan `admin` şifresini değiştirin (veya hesabı silin).
- [ ] PostgreSQL `postgres` ve `wolfteam` rollerine güçlü parolalar verin.
- [ ] PostgreSQL'in sadece `127.0.0.1:5432` dinlediğinden emin olun.
- [ ] `.env` veya `start.ps1` dosyalarını kesinlikle Git reposuna eklemeyin.

---

## 7. Ortam Değişkenleri Referansı

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `WT_DATABASE_URL` | `postgres://...` | PostgreSQL bağlantı adresi |
| `WT_LISTEN_HOST` | `0.0.0.0` | Dinlenecek yerel ağ arayüzü |
| `WT_ADVERTISED_IP` | `127.0.0.1` | İstemcilere bildirilen sunucu IPv4 adresi |
| `WT_PUBLIC_IP` | `127.0.0.1` | UDP/NAT eşleşmesinde kullanılan genel IPv4 |
| `WT_CLIENT_2012` | `false` | `true` yapıldığında Eylül 2012 TR protokolünü etkinleştirir |
| `WT_WPACK_ENABLED` | `false` (Docker: `true`) | W-Pack / K-Paket satışını açar (`2012` için `false` önerilir) |
| `WT_PEER_ENDPOINT_MODE` | `1` (Docker: `2`) | `2` = oda bazlı sunucu UDP relay (VDS/NAT için önerilir) |
| `WT_BROKER_PORT` | `40706` | Broker TCP portu |
| `WT_GAME_PORT` | `40707` | Oyun TCP & UDP portu |
| `WT_BOT_PEER_PORT_BASE` | `GamePort + 1` (`40708`) | Bot UDP bootstrap portu |
| `WT_BUDDY_PORT` | `40709` | Arkadaşlık sunucusu TCP portu |
| `WT_RELAY_PORT` | `40710` | Genel UDP relay portu |
| `WT_RELAY_PORT_BASE` | `41000` | Oda bazlı UDP relay port aralığının başlangıcı |
| `WT_RANKING_PORT` | `0` (Docker: `40719`) | Oyun içi sıralama HTTP portu (`0` = kapalı) |
| `WT_PATCH_ENABLED` | `true` | HTTP Patcher / `/register` / `/healthz` servisini açar |
| `WT_PATCH_PORT` | `8080` | HTTP Patcher dinleme portu |
| `WT_LOGIN_PORT` | `8444` | Launcher TLS giriş portu |
| `WT_SHOPDATA_PATH` | *(boş)* | `shopdata.dat` yolu (`setitemdata.dat` aynı klasörde aranır) |
| `WT_LUCKYBOXDATA_PATH` | *(boş)* | `bighitboxitem.dat` yolu |
| `WT_NOTICE_WEB_DIR` | *(boş)* | `notice/` klasörü yolu |
| `WT_APEX_WEB_DIR` | *(boş)* | Avital/Apex statik dosyaları klasörü |
| `WT_EVENT_TOP` | `0` | Lobi üst afişi (`event_topN.dtx`, `0` = gizli) |

---

## 8. Sık Karşılaşılan Hatalar ve Çözümleri

### 1. Kanal seçince "Geçersiz şifre" hatası
- **Neden:** `Wolfteam.bin` doğrudan düz metin şifreyle veya yanlış argüman sırasıyla (ör. `_!!DEBUG!!_ ...`) çağrılmıştır.
- **Çözüm:** `Wolfteam.bin` yalnızca **2 argüman** bekler: `Wolfteam.bin <kullanici_adi> <sifre_utf8_hex>`. `launcher/WolfTeamLauncher.cs` (DirectBin modu) veya `client-launcher/wt_launcher.py` bunu otomatik uygular.

### 2. Avital `file update failure [11071,-1,0,0] DF:update.u`
- **Neden:** `webd.xoe`, `HEAD` ve ardından `Range: bytes=...` başlığıyla `GET` isteği gönderir; sunucu `206 Partial Content` + `Accept-Ranges: bytes` döndürmezse güncelleme başarısız olur.
- **Çözüm:** `WolfTeamLauncher.exe` içinde **Avital / update.u Bypass** seçeneğini açık bırakın ve yerel makinede Port 80'in başka bir uygulama tarafından tutulmadığından emin olun.

### 3. Odaya girince kendiniz görünmüyorsunuz; "Hazır" ve "Çıkış" tuşları tepki vermiyor
- **Neden:** Eylül 2012 TR istemcisiyle `WT_CLIENT_2012=false` (2013 modunda) çalışan sunucuya bağlandınız. `0x1605` (RoomRoster) paketindeki `WeaponFlags` ve `Slot` sonrası 4 bayt kayması istemcinin oda durumunu bozar.
- **Çözüm:** Sunucuda `WT_CLIENT_2012=true` (ve `WT_WPACK_ENABLED=false`) ayarlayıp sunucuyu yeniden başlatın.

### 4. Sunucu listesi geliyor ama kanala / maça bağlanılamıyor
- **Neden:** `WT_ADVERTISED_IP` ve `WT_PUBLIC_IP` değeri `127.0.0.1` kalmış veya `40707` TCP/UDP, `40708` UDP, `41000-41099` UDP portları bulut VCN/firewall tarafında kapalıdır.
- **Çözüm:** Her iki IP değişkenine sunucunun gerçek genel IPv4 adresini yazın ve bulut güvenlik kurallarını kontrol edin.

### 5. SSH bağlantısını kapatınca PostgreSQL veya oyun sunucusu duruyor (Windows VDS)
- **Neden:** Windows OpenSSH oturumunda doğrudan başlatılan süreçler oturum kapanınca sonlandırılır.
- **Çözüm:** PostgreSQL'i Windows Servisi (`WolfTeam-Postgres17`), `wtserver.exe` sürecini ise Windows Zamanlanmış Görevi (`WolfTeam-Go-Server`) olarak çalıştırın.
