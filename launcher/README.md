# WolfTeam 2012/2013 Windows Launcher (C# WinForms)

Visual Studio gerektirmeden Windows'un yerleşik .NET Framework 4.x derleyicisi (`csc.exe`) ile derlenebilen özel başlatıcıdır.

## Ne İşe Yarar?

1. **Doğrudan `Wolfteam.bin` Başlatma:** Şifreyi istemcinin beklediği UTF-8 hex biçiminde geçirir (`Wolfteam.bin <kullanici_adi> <utf8_hex_sifre>`). `NyxLauncher` / yama sunucusu hatalarını atlar.
2. **Yerel Avital / `update.u` HTTP Sunucusu:** `127.0.0.1:80` üzerinde `HEAD`, `GET`, `Accept-Ranges: bytes` ve `206 Partial Content` destekleyen yerel HTTP sunucusu açar ve eski Joygame CDN alan adlarını `hosts` üzerinden yerelde karşılar.
3. **2012 İstemci Broker Yönlendirme (Loopback Relay):** Eylül 2012 TR istemcisinde gömülü eski broker IP'sini (`212.68.41.11:30706`) yalnızca Windows `Loopback Pseudo-Interface 1` (`InterfaceIndex 1`, `-SkipAsSource $true`) üzerine geçici olarak bağlar ve hedef sunucuya aktarır. Ethernet/Wi-Fi DHCP ayarlarına dokunmaz; launcher kapatıldığında loopback kaydını otomatik temizler.

## Derleme

Bu klasörde çift tıklayarak veya komut isteminde çalıştırın:

```bat
build.bat
```

Oluşan `WolfTeamLauncher.exe` dosyasını yönetici olarak çalıştırın (Port 80 dinleme, `hosts` ve Loopback IP kaydı için gereklidir).
