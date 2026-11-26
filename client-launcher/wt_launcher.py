import json
import os
import re
import subprocess
import sys
import tkinter as tk
import urllib.error
import urllib.parse
import urllib.request
from http.cookiejar import CookieJar
from tkinter import messagebox

# Settings file path (always next to this script, independent of the CWD)
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
SETTINGS_FILE = os.path.join(SCRIPT_DIR, "launcher_settings.json")

# The game container's patcher HTTP service serves the account registration
# page and the health endpoint. Port 8080 matches the compose port mapping.
#
# This is the public address of the test server rather than a loopback one:
# the same value works from this machine and from every other PC, so one
# setting covers both. Only registration and the pre-flight check use it --
# the game itself finds the server through
# Img\wt_interface\attributes\server\server.txt.
DEFAULT_SERVER_URL = "http://127.0.0.1:8080"

# Mirrors the server-side registration rules (internal/patcher/registration.go):
# publicUsernamePattern and the 8..64 byte password bounds.
USERNAME_PATTERN = re.compile(r"^[A-Za-z0-9_]{3,20}$")
MIN_PASSWORD_BYTES = 8
MAX_PASSWORD_BYTES = 64

REGISTER_TIMEOUT_SECONDS = 15


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    # The registration endpoint signals success with a 303 redirect to
    # /register?created=1; we need the raw status instead of following it.
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def check_server(server_url):
    """Report whether the server is up, before the client is started.

    Without this the client launches, fails to reach the broker and closes
    itself without a word, which reads as the game being broken rather than
    the server being down.
    """
    base = server_url.rstrip("/")
    try:
        with urllib.request.urlopen(
            base + "/healthz", timeout=REGISTER_TIMEOUT_SECONDS
        ) as response:
            if response.getcode() == 200:
                return True, ""
            return False, "Sunucu hazır değil (sunucu yanıtı: %d)." % response.getcode()
    except urllib.error.HTTPError as error:
        status = error.code
        error.close()
        return False, "Sunucu sağlık kontrolü başarısız (sunucu yanıtı: %d)." % status
    except urllib.error.URLError as error:
        reason = getattr(error, "reason", error)
        return False, "Sunucuya bağlanılamadı (%s):\n%s" % (base, reason)
    except Exception as error:  # noqa: BLE001 - surfaced to the user in a dialog
        return False, "Sunucu kontrolünde beklenmeyen bir hata oluştu:\n%s" % error


def register_account(server_url, username, password, password_confirmation):
    """Create an account through the server's /register endpoint.

    Returns (success: bool, message: str). The two-step flow is required:
    GET /register establishes the wt_registration_csrf cookie, and the form
    token must match that cookie on POST or the server rejects with 403.
    """
    base = server_url.rstrip("/")
    try:
        jar = CookieJar()
        opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(jar), _NoRedirect()
        )

        page = opener.open(base + "/register", timeout=REGISTER_TIMEOUT_SECONDS)
        html = page.read().decode("utf-8", "replace")
        page.close()

        match = re.search(r'name="csrf_token"\s+value="([^"]+)"', html)
        if not match:
            return False, "Sunucudan kayıt formu alınamadı (/register)."
        csrf_token = match.group(1)

        form = urllib.parse.urlencode({
            "username": username,
            "password": password,
            "password_confirmation": password_confirmation,
            "account_notice": "accepted",
            "csrf_token": csrf_token,
        }).encode("utf-8")
        request = urllib.request.Request(
            base + "/register",
            data=form,
            headers={"Content-Type": "application/x-www-form-urlencoded"},
            method="POST",
        )
        try:
            response = opener.open(request, timeout=REGISTER_TIMEOUT_SECONDS)
            status = response.getcode()
            response.close()
        except urllib.error.HTTPError as error:
            status = error.code
            retry_after = error.headers.get("Retry-After") if error.headers else None
            error.close()

        if status == 303:
            return True, "Hesabınız oluşturuldu! Artık giriş yapıp oyunu başlatabilirsiniz."
        if status == 409:
            return False, "Bu kullanıcı adı zaten alınmış. Lütfen başka bir ad seçin."
        if status == 422:
            return False, (
                "Girilen bilgiler sunucu kurallarına uymuyor:\n"
                "- Kullanıcı adı: 3-20 harf, rakam veya alt çizgi\n"
                "- Şifre: 8-64 karakter\n"
                "- Şifreler birbiriyle aynı olmalı"
            )
        if status == 429:
            wait = ""
            if retry_after:
                minutes = -(-int(retry_after) // 60)
                wait = f" (~{minutes} dakika sonra tekrar deneyin)"
            return False, "Çok fazla kayıt denemesi yapıldı." + wait
        if status == 403:
            return False, "Kayıt oturumu zaman aşımına uğradı. Lütfen tekrar deneyin."
        if status == 503:
            return False, "Sunucuda kayıt şu anda kullanılamıyor. Sonra tekrar deneyin."
        return False, f"Kayıt başarısız (sunucu yanıtı: {status})."
    except urllib.error.URLError as error:
        reason = getattr(error, "reason", error)
        return False, f"Sunucuya bağlanılamadı ({base}):\n{reason}"
    except Exception as error:  # noqa: BLE001 - surfaced to the user in a dialog
        return False, f"Beklenmeyen bir hata oluştu:\n{error}"


class WolfteamLauncher:
    def __init__(self, root):
        self.root = root
        self.root.title("Wolfteam Özel Sunucu Başlatıcı")
        self.root.geometry("420x430")
        self.root.configure(bg="#0f111a")
        self.root.resizable(False, False)

        # Default settings.
        #
        # No seeded credentials: a clean database has no game account called
        # "admin", so pre-filling one only produced a wrong-password refusal on
        # the first run. Empty fields send the player to KAYIT OL instead.
        self.settings = {
            "username": "",
            "password": "",
            "server_url": DEFAULT_SERVER_URL,
        }
        self.load_settings()

        self.setup_ui()

    def load_settings(self):
        if os.path.exists(SETTINGS_FILE):
            try:
                with open(SETTINGS_FILE, "r", encoding="utf-8") as f:
                    data = json.load(f)
                    # Only keep the fields we still use
                    if "username" in data:
                        self.settings["username"] = data["username"]
                    if "password" in data:
                        self.settings["password"] = data["password"]
                    if "server_url" in data and data["server_url"]:
                        self.settings["server_url"] = data["server_url"]
            except Exception:
                pass

    def save_settings(self):
        self.settings["username"] = self.entry_user.get().strip()
        self.settings["password"] = self.entry_pass.get().strip()
        try:
            with open(SETTINGS_FILE, "w", encoding="utf-8") as f:
                json.dump(self.settings, f, indent=4)
        except Exception:
            pass

    def setup_ui(self):
        # Header Label
        self.label_header = tk.Label(
            self.root,
            text="WOLFTEAM",
            font=("Consolas", 28, "bold"),
            fg="#00f0ff",
            bg="#0f111a"
        )
        self.label_header.pack(pady=(25, 2))

        self.label_sub = tk.Label(
            self.root,
            text="Özel Sunucu Başlatıcı v1.4",
            font=("Arial", 10, "italic"),
            fg="#8f9cae",
            bg="#0f111a"
        )
        self.label_sub.pack(pady=(0, 15))

        # Form Frame
        form_frame = tk.Frame(self.root, bg="#151824", bd=1, relief="solid", highlightbackground="#313652", highlightthickness=1)
        form_frame.pack(padx=30, pady=10, fill="both", expand=True)

        # User input
        lbl_user = tk.Label(form_frame, text="Kullanıcı Adı:", font=("Arial", 10, "bold"), fg="#ffffff", bg="#151824")
        lbl_user.pack(anchor="w", padx=20, pady=(20, 2))
        self.entry_user = tk.Entry(form_frame, font=("Arial", 11), bg="#1e2235", fg="#ffffff", insertbackground="white", bd=0, highlightthickness=1, highlightbackground="#313652", highlightcolor="#00f0ff")
        self.entry_user.pack(fill="x", padx=20, ipady=5)
        self.entry_user.insert(0, self.settings["username"])

        # Password input
        lbl_pass = tk.Label(form_frame, text="Şifre:", font=("Arial", 10, "bold"), fg="#ffffff", bg="#151824")
        lbl_pass.pack(anchor="w", padx=20, pady=(15, 2))
        self.entry_pass = tk.Entry(form_frame, font=("Arial", 11), bg="#1e2235", fg="#ffffff", insertbackground="white", bd=0, highlightthickness=1, highlightbackground="#313652", highlightcolor="#00f0ff", show="*")
        self.entry_pass.pack(fill="x", padx=20, ipady=5)
        self.entry_pass.insert(0, self.settings["password"])

        # Start Button
        self.btn_launch = tk.Button(
            self.root,
            text="OYUNU BAŞLAT",
            font=("Arial", 12, "bold"),
            bg="#00b8e6",
            fg="#ffffff",
            activebackground="#00f0ff",
            activeforeground="#000000",
            bd=0,
            cursor="hand2",
            command=self.launch_game
        )
        self.btn_launch.pack(fill="x", padx=30, pady=(15, 8), ipady=10)

        # Register Button
        self.btn_register = tk.Button(
            self.root,
            text="YENİ HESAP OLUŞTUR (KAYIT OL)",
            font=("Arial", 10, "bold"),
            bg="#232842",
            fg="#00f0ff",
            activebackground="#313652",
            activeforeground="#ffffff",
            bd=0,
            cursor="hand2",
            command=self.open_register_window
        )
        self.btn_register.pack(fill="x", padx=30, pady=(0, 25), ipady=6)

    def open_register_window(self):
        RegisterWindow(self.root, self)

    def on_registered(self, username, password):
        self.entry_user.delete(0, tk.END)
        self.entry_user.insert(0, username)
        self.entry_pass.delete(0, tk.END)
        self.entry_pass.insert(0, password)
        self.save_settings()

    def launch_game(self):
        username = self.entry_user.get().strip()
        password = self.entry_pass.get()

        if not username:
            messagebox.showerror(
                "Hata",
                "Lütfen kullanıcı adını girin veya önce KAYIT OL ile hesap oluşturun!",
            )
            return
        if not password:
            messagebox.showerror(
                "Hata",
                "Lütfen şifrenizi girin veya önce KAYIT OL ile hesap oluşturun!",
            )
            return

        server_url = self.settings.get("server_url", DEFAULT_SERVER_URL).strip()
        ready, message = check_server(server_url)
        if not ready:
            messagebox.showerror(
                "Sunucu kullanılamıyor",
                message
                + "\n\nÖnce WolfTeam sunucusunu başlatın veya"
                + " launcher_settings.json içindeki adresi kontrol edin.",
            )
            return

        self.save_settings()

        # Make sure we run from the game folder (where Wolfteam.bin lives)
        cur_dir = os.path.dirname(os.path.abspath(__file__))
        bin_file = "Wolfteam.bin"
        if not os.path.exists(os.path.join(cur_dir, bin_file)):
            for candidate in ("WolfClient", "WolfTeamTS"):
                alt_dir = os.path.join(os.path.dirname(cur_dir), candidate)
                if os.path.exists(os.path.join(alt_dir, bin_file)):
                    cur_dir = alt_dir
                    break
        os.chdir(cur_dir)

        # Check Wolfteam.bin existence
        if not os.path.exists(bin_file):
            messagebox.showerror("Hata", f"Klasörde {bin_file} bulunamadı!\nAranan yol: {cur_dir}")
            return

        # CShell decodes the second argument two hexadecimal digits at a time
        # (sscanf "%2x" per byte) and serializes the result as the UInt16
        # length-prefixed password field of the 0x1300 channel login message,
        # so the password must be passed as its UTF-8 hex encoding.
        encoded_password = password.encode("utf-8").hex()
        cmd = [bin_file, username, encoded_password]
        try:
            subprocess.Popen(cmd, creationflags=subprocess.CREATE_NEW_CONSOLE)
            self.root.destroy()
        except Exception as e:
            try:
                exe_file = "WolfteamRun.exe"
                import shutil
                shutil.copy2(bin_file, exe_file)
                cmd_exe = [exe_file, username, encoded_password]
                subprocess.Popen(cmd_exe, creationflags=subprocess.CREATE_NEW_CONSOLE)
                self.root.destroy()
            except Exception as e2:
                messagebox.showerror("Hata", f"Oyun başlatılamadı:\n{e}\n\nAlternatif başlatma hatası:\n{e2}")


class RegisterWindow:
    def __init__(self, parent, launcher):
        self.launcher = launcher

        self.window = tk.Toplevel(parent)
        self.window.title("Yeni Hesap Oluştur")
        self.window.geometry("420x430")
        self.window.configure(bg="#0f111a")
        self.window.resizable(False, False)
        self.window.grab_set()

        lbl_title = tk.Label(
            self.window,
            text="HESAP KAYDI",
            font=("Consolas", 18, "bold"),
            fg="#00f0ff",
            bg="#0f111a"
        )
        lbl_title.pack(pady=(20, 2))

        lbl_sub = tk.Label(
            self.window,
            text="Sunucuda yeni bir oyun hesabı oluşturun.",
            font=("Arial", 9, "italic"),
            fg="#8f9cae",
            bg="#0f111a"
        )
        lbl_sub.pack(pady=(0, 12))

        form_frame = tk.Frame(self.window, bg="#151824", bd=1, relief="solid", highlightbackground="#313652", highlightthickness=1)
        form_frame.pack(padx=30, pady=5, fill="both", expand=True)

        lbl_user = tk.Label(form_frame, text="Kullanıcı Adı:", font=("Arial", 10, "bold"), fg="#ffffff", bg="#151824")
        lbl_user.pack(anchor="w", padx=20, pady=(18, 2))
        self.entry_user = tk.Entry(form_frame, font=("Arial", 11), bg="#1e2235", fg="#ffffff", insertbackground="white", bd=0, highlightthickness=1, highlightbackground="#313652", highlightcolor="#00f0ff")
        self.entry_user.pack(fill="x", padx=20, ipady=5)

        lbl_hint_user = tk.Label(form_frame, text="3-20 harf, rakam veya alt çizgi (_)", font=("Arial", 8), fg="#8f9cae", bg="#151824")
        lbl_hint_user.pack(anchor="w", padx=20, pady=(1, 8))

        lbl_pass = tk.Label(form_frame, text="Şifre:", font=("Arial", 10, "bold"), fg="#ffffff", bg="#151824")
        lbl_pass.pack(anchor="w", padx=20, pady=(0, 2))
        self.entry_pass = tk.Entry(form_frame, font=("Arial", 11), bg="#1e2235", fg="#ffffff", insertbackground="white", bd=0, highlightthickness=1, highlightbackground="#313652", highlightcolor="#00f0ff", show="*")
        self.entry_pass.pack(fill="x", padx=20, ipady=5)

        lbl_pass2 = tk.Label(form_frame, text="Şifre (Tekrar):", font=("Arial", 10, "bold"), fg="#ffffff", bg="#151824")
        lbl_pass2.pack(anchor="w", padx=20, pady=(12, 2))
        self.entry_pass2 = tk.Entry(form_frame, font=("Arial", 11), bg="#1e2235", fg="#ffffff", insertbackground="white", bd=0, highlightthickness=1, highlightbackground="#313652", highlightcolor="#00f0ff", show="*")
        self.entry_pass2.pack(fill="x", padx=20, ipady=5)

        lbl_hint_pass = tk.Label(form_frame, text="En az 8, en fazla 64 karakter", font=("Arial", 8), fg="#8f9cae", bg="#151824")
        lbl_hint_pass.pack(anchor="w", padx=20, pady=(1, 12))

        self.btn_register = tk.Button(
            self.window,
            text="HESABI OLUŞTUR",
            font=("Arial", 11, "bold"),
            bg="#00b8e6",
            fg="#ffffff",
            activebackground="#00f0ff",
            activeforeground="#000000",
            bd=0,
            cursor="hand2",
            command=self.submit
        )
        self.btn_register.pack(fill="x", padx=30, pady=(12, 20), ipady=8)

        self.window.bind("<Return>", lambda event: self.submit())

    def submit(self):
        username = self.entry_user.get().strip()
        password = self.entry_pass.get()
        confirmation = self.entry_pass2.get()

        # Same rules the server enforces on /register; check locally first so
        # obvious mistakes do not burn the server's 5-attempts/10-min limit.
        if not USERNAME_PATTERN.match(username):
            messagebox.showerror(
                "Hata",
                "Kullanıcı adı 3-20 karakter olmalı ve yalnızca\nharf, rakam veya alt çizgi (_) içermeli.",
                parent=self.window
            )
            return
        if not (MIN_PASSWORD_BYTES <= len(password.encode("utf-8")) <= MAX_PASSWORD_BYTES):
            messagebox.showerror(
                "Hata",
                "Şifre en az 8, en fazla 64 karakter olmalı.",
                parent=self.window
            )
            return
        if password != confirmation:
            messagebox.showerror("Hata", "Şifreler birbiriyle uyuşmuyor.", parent=self.window)
            return

        self.btn_register.config(state="disabled", text="KAYDEDİLİYOR...")
        self.window.update_idletasks()
        try:
            success, message = register_account(
                self.launcher.settings.get("server_url", DEFAULT_SERVER_URL),
                username,
                password,
                confirmation
            )
        finally:
            self.btn_register.config(state="normal", text="HESABI OLUŞTUR")

        if success:
            self.launcher.on_registered(username, password)
            messagebox.showinfo("Kayıt Başarılı", message, parent=self.window)
            self.window.destroy()
        else:
            messagebox.showerror("Kayıt Başarısız", message, parent=self.window)


if __name__ == "__main__":
    root = tk.Tk()
    app = WolfteamLauncher(root)
    root.mainloop()
