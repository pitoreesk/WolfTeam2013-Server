# WolfTeam 2013 TR Python Başlatıcı (`wt_launcher.py`)

2013 TR istemcisi için dahili hesap kayıt (`/register`) ve sunucu sağlık kontrolü (`/healthz`) özellikli hafif Python Tkinter başlatıcısıdır.

## Kullanım

1. Bu klasördeki dosyaları (`wt_launcher.py`, `launcher_settings.json`, `baslat_launcher.bat`) oyun klasörüne (veya oyun klasörünün yanına) kopyalayın.
2. `launcher_settings.json` içindeki `server_url` değerini sunucunuzun HTTP Patcher adresine göre güncelleyin:
   ```json
   {
       "username": "",
       "password": "",
       "server_url": "http://127.0.0.1:8080"
   }
   ```
3. Oyun istemcisindeki `Img\wt_interface\attributes\server\server.txt` dosyasının aynı sunucu IP'sini gösterdiğinden emin olun.
4. `baslat_launcher.bat` dosyasına çift tıklayarak (Python 3 kurulu olmalıdır) başlatıcıyı açın.
5. **YENİ HESAP OLUŞTUR (KAYIT OL)** ile hesap oluşturabilir ve **OYUNU BAŞLAT** ile `Wolfteam.bin` istemcisini doğru UTF-8 hex parola formatıyla başlatabilirsiniz.

> **Not:** Eylül 2012 TR istemcisi için Avital `update.u` bypass ve gömülü broker IP yönlendirmesi gerektiğinden kök dizindeki [`launcher/`](../launcher/README.md) (C# WinForms) başlatıcısını kullanın.
