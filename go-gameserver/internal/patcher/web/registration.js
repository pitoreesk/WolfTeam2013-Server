(() => {
  const form = document.querySelector("[data-registration-form]");
  if (!form) return;

  const password = form.elements.password;
  const confirmation = form.elements.password_confirmation;
  const meter = form.querySelector(".password-meter");
  const match = form.querySelector("[data-password-match]");

  const passwordScore = (value) => {
    if (!value) return 0;
    let score = value.length >= 8 ? 1 : 0;
    if (value.length >= 12) score += 1;
    if (/[a-z]/.test(value) && /[A-Z]/.test(value)) score += 1;
    if (/\d/.test(value) && /[^A-Za-z0-9]/.test(value)) score += 1;
    return Math.min(4, score);
  };

  const updatePassword = () => {
    meter.dataset.score = String(passwordScore(password.value));
    if (!confirmation.value) {
      confirmation.setCustomValidity("");
      match.textContent = "Enter the same password again";
      return;
    }
    const matches = password.value === confirmation.value;
    confirmation.setCustomValidity(matches ? "" : "The passwords do not match.");
    match.textContent = matches ? "Passwords match" : "Passwords do not match";
  };

  password.addEventListener("input", updatePassword);
  confirmation.addEventListener("input", updatePassword);

  form.querySelectorAll("[data-reveal]").forEach((button) => {
    button.addEventListener("click", () => {
      const input = document.getElementById(button.dataset.reveal);
      const showing = input.type === "text";
      input.type = showing ? "password" : "text";
      button.textContent = showing ? "SHOW" : "HIDE";
      button.setAttribute("aria-pressed", String(!showing));
      button.setAttribute("aria-label", showing ? "Show password" : "Hide password");
    });
  });

  form.addEventListener("submit", (event) => {
    updatePassword();
    if (!form.checkValidity()) {
      event.preventDefault();
      form.reportValidity();
      return;
    }
    const submit = form.querySelector(".submit");
    submit.disabled = true;
    submit.querySelector("span").textContent = "PROVISIONING ACCOUNT";
  });
})();
