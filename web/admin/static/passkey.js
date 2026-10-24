/* GoPress passkeys (WebAuthn): sign-in, second factor, re-authentication
   and registration. Elements declare what they do with data attributes. */
(function () {
	'use strict';
	document.body.classList.remove('no-js');
	document.body.classList.add('js');
	if (!window.PublicKeyCredential || !navigator.credentials) { return; }

	function b64urlToBuf(s) {
		s = s.replace(/-/g, '+').replace(/_/g, '/');
		while (s.length % 4) { s += '='; }
		var bin = atob(s), buf = new Uint8Array(bin.length);
		for (var i = 0; i < bin.length; i++) { buf[i] = bin.charCodeAt(i); }
		return buf.buffer;
	}
	function bufToB64url(buf) {
		var bytes = new Uint8Array(buf), s = '';
		for (var i = 0; i < bytes.length; i++) { s += String.fromCharCode(bytes[i]); }
		return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
	}

	function requestOptions(json) {
		var pk = json.publicKey;
		if (PublicKeyCredential.parseRequestOptionsFromJSON) { return PublicKeyCredential.parseRequestOptionsFromJSON(pk); }
		pk.challenge = b64urlToBuf(pk.challenge);
		(pk.allowCredentials || []).forEach(function (c) { c.id = b64urlToBuf(c.id); });
		return pk;
	}
	function creationOptions(json) {
		var pk = json.publicKey;
		if (PublicKeyCredential.parseCreationOptionsFromJSON) { return PublicKeyCredential.parseCreationOptionsFromJSON(pk); }
		pk.challenge = b64urlToBuf(pk.challenge);
		pk.user.id = b64urlToBuf(pk.user.id);
		(pk.excludeCredentials || []).forEach(function (c) { c.id = b64urlToBuf(c.id); });
		return pk;
	}
	function credentialJSON(cred) {
		if (cred.toJSON) { try { return cred.toJSON(); } catch (e) {} }
		var r = cred.response, out = { id: cred.id, rawId: bufToB64url(cred.rawId), type: cred.type, response: {},
			clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {} };
		if (cred.authenticatorAttachment) { out.authenticatorAttachment = cred.authenticatorAttachment; }
		out.response.clientDataJSON = bufToB64url(r.clientDataJSON);
		if (r.attestationObject) {
			out.response.attestationObject = bufToB64url(r.attestationObject);
			if (r.getTransports) { out.response.transports = r.getTransports(); }
		} else {
			out.response.authenticatorData = bufToB64url(r.authenticatorData);
			out.response.signature = bufToB64url(r.signature);
			if (r.userHandle) { out.response.userHandle = bufToB64url(r.userHandle); }
		}
		return out;
	}

	function post(url, csrf, body) {
		return fetch(url, {
			method: 'POST', credentials: 'same-origin',
			headers: { 'X-CSRF-Token': csrf, 'Accept': 'application/json', 'Content-Type': 'application/json' },
			body: body ? JSON.stringify(body) : '{}'
		}).then(function (r) {
			return r.json().catch(function () { return {}; }).then(function (j) {
				if (!r.ok) { var e = new Error(j.error || 'Request failed'); e.data = j; throw e; }
				return j;
			});
		});
	}

	function showError(el, err) {
		var p = el.querySelector('.gp-passkey-error');
		if (err && err.data && err.data.redirect) { location.href = err.data.redirect; return; }
		var msg = err && err.name === 'NotAllowedError' ? 'The passkey request was cancelled or timed out.' :
			(err && err.message) || 'Something went wrong.';
		if (p) { p.textContent = msg; p.hidden = false; }
	}
	function busy(btn, on) { if (btn) { btn.disabled = on; btn.classList.toggle('updating-message', on); } }

	// Login page: button plus passkey autofill in the username field.
	document.querySelectorAll('[data-passkey-login]').forEach(function (el) {
		el.hidden = false;
		var csrf = el.getAttribute('data-csrf');
		var btn = el.querySelector('button');
		var conditional = null;
		function finish(cred) {
			var remember = document.getElementById('rememberme');
			var q = '?redirect_to=' + encodeURIComponent(el.getAttribute('data-redirect') || '/admin/') +
				(remember && remember.checked ? '&remember=1' : '');
			return post('/login/passkey/finish' + q, csrf, credentialJSON(cred)).then(function (j) { location.href = j.redirect; });
		}
		function start(mediation) {
			return post('/login/passkey/begin', csrf).then(function (opts) {
				var req = { publicKey: requestOptions(opts) };
				if (mediation) {
					conditional = new AbortController();
					req.mediation = 'conditional';
					req.signal = conditional.signal;
				}
				return navigator.credentials.get(req);
			}).then(function (cred) { if (cred) { return finish(cred); } });
		}
		btn.addEventListener('click', function () {
			if (conditional) { conditional.abort(); conditional = null; }
			busy(btn, true);
			start(false).catch(function (e) { busy(btn, false); showError(el, e); });
		});
		if (PublicKeyCredential.isConditionalMediationAvailable) {
			PublicKeyCredential.isConditionalMediationAvailable().then(function (ok) {
				if (ok) { start(true).catch(function (e) { if (e.name !== 'AbortError') { showError(el, e); } }); }
			});
		}
	});

	// Second factor / re-authentication with a passkey.
	document.querySelectorAll('[data-passkey-assert]').forEach(function (el) {
		var base = el.getAttribute('data-passkey-assert');
		var csrf = el.getAttribute('data-csrf');
		var btn = el.querySelector('button');
		btn.addEventListener('click', function () {
			busy(btn, true);
			post(base + '/begin', csrf).then(function (opts) {
				return navigator.credentials.get({ publicKey: requestOptions(opts) });
			}).then(function (cred) {
				var q = '?redirect_to=' + encodeURIComponent(el.getAttribute('data-redirect') || '/admin/') +
					'&next=' + encodeURIComponent(el.getAttribute('data-redirect') || '/admin/');
				return post(base + '/finish' + q, csrf, credentialJSON(cred));
			}).then(function (j) { location.href = j.redirect; }).catch(function (e) { busy(btn, false); showError(el, e); });
		});
	});

	// Registration of a new passkey.
	document.querySelectorAll('[data-passkey-register]').forEach(function (el) {
		var base = el.getAttribute('data-passkey-register');
		var csrf = el.getAttribute('data-csrf');
		var btn = el.querySelector('button');
		var name = el.querySelector('input');
		btn.addEventListener('click', function () {
			busy(btn, true);
			post(base + '/begin', csrf).then(function (opts) {
				return navigator.credentials.create({ publicKey: creationOptions(opts) });
			}).then(function (cred) {
				var label = (name && name.value.trim()) || guessName();
				return post(base + '/finish?name=' + encodeURIComponent(label), csrf, credentialJSON(cred));
			}).then(function (j) { location.href = j.redirect; }).catch(function (e) { busy(btn, false); showError(el, e); });
		});
	});

	function guessName() {
		var ua = navigator.userAgent;
		if (/iPhone|iPad/.test(ua)) { return 'iPhone/iPad'; }
		if (/Android/.test(ua)) { return 'Android'; }
		if (/Mac OS X/.test(ua)) { return 'Mac'; }
		if (/Windows/.test(ua)) { return 'Windows'; }
		if (/Linux/.test(ua)) { return 'Linux'; }
		return 'Passkey';
	}
})();
