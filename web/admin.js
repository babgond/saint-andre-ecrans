(function () {
  'use strict';
  if (document.body.classList.contains('login')) {
    document.getElementById('login-error').hidden = new URLSearchParams(location.search).get('connexion') !== 'refusee';
    return;
  }

  const labels = {
    active: 'En diffusion', scheduled: 'Programmée', expired: 'Expirée',
    draft: 'Brouillon', disabled: 'Désactivée', trash: 'Dans la corbeille'
  };
  const views = {
    'tableau-de-bord': 'Tableau de bord', bibliotheque: 'Bibliothèque',
    programmation: 'Programmation', previsualisation: 'Prévisualisation', parametres: 'Paramètres'
  };
  const content = document.getElementById('content');
  const dialog = document.getElementById('dialog');
  const notice = document.getElementById('notice');
  let data, view, inTrash = false, chosen = new Set(), month, noticeTimeout, dragID, requestSequence = 0;
  const sizes = new Intl.NumberFormat('fr-FR', { maximumFractionDigits: 1 });
  const dateFmt = new Intl.DateTimeFormat('fr-FR', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Paris' });
  const monthFmt = new Intl.DateTimeFormat('fr-FR', { year: 'numeric', month: 'long', timeZone: 'UTC' });

  function html(value) {
    return String(value ?? '').replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
  }
  function bytes(n) {
    const units = ['o', 'Kio', 'Mio', 'Gio', 'Tio'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return sizes.format(n) + ' ' + units[i];
  }
  function localDate(s) {
    return s ? new Intl.DateTimeFormat('fr-FR', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }).format(new Date(s + 'Z')) : '';
  }
  function media(p, mini) { return '/api/admin/image/' + encodeURIComponent(p.file) + (mini ? '?mini=1' : ''); }
  function button(label, action, cls = 'secondary', attrs = '') {
    return '<button type="button" class="button ' + cls + '" data-action="' + action + '" ' + attrs + '>' + label + '</button>';
  }
  function showNotice(message) {
    clearTimeout(noticeTimeout);
    notice.textContent = message;
    noticeTimeout = setTimeout(() => { notice.textContent = ''; }, 6500);
  }
  async function api(path, options = {}) {
    return readResponse(await fetch(path, options));
  }
  async function readResponse(response) {
    if (response.status === 401) {
      location.assign('/admin');
      throw new Error('Votre session a expiré.');
    }
    if (!response.ok) {
      const result = await response.json().catch(() => ({}));
      throw new Error(result.error || 'La demande a échoué. Réessayez.');
    }
    if (response.status !== 204) return response.json();
  }
  const json = (path, method, value) => api('/api/admin/' + path, {
    method, body: JSON.stringify(value), headers: { 'Content-Type': 'application/json' }
  });
  function sendFile(path, file, onProgress) {
    const name = encodeURIComponent(file.name).replace(/['()*]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase());
    const url = '/api/admin/' + path;
    const disposition = "attachment; filename*=UTF-8''" + name;
    if (!onProgress) return api(url, {
      method: 'POST', body: file, headers: { 'Content-Disposition': disposition }
    });
    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest();
      request.open('POST', url);
      request.setRequestHeader('Content-Disposition', disposition);
      request.upload.onprogress = e => onProgress(e.loaded, false);
      request.upload.onload = () => onProgress(file.size, true);
      request.onload = () => {
        const body = request.status === 204 ? null : request.responseText;
        readResponse(new Response(body, { status: request.status })).then(resolve, reject);
      };
      request.onerror = request.onabort = () => reject(new Error('L’envoi a été interrompu. Réessayez.'));
      request.send(file);
    });
  }
  function route() {
    const next = location.hash.slice(1);
    view = Object.hasOwn(views, next) ? next : 'tableau-de-bord';
    document.getElementById('location').textContent = views[view];
    document.querySelectorAll('nav a').forEach(a => {
      if (a.dataset.view === view) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    });
    render();
  }
  async function refresh() {
    const sequence = ++requestSequence;
    const fresh = await api('/api/admin/state');
    if (sequence !== requestSequence) return;
    data = fresh;
    document.getElementById('display-name').textContent = data.displayName;
    document.title = 'Administration · ' + data.displayName + ' Écrans';
    document.getElementById('account').textContent = data.account;
    document.getElementById('initials').textContent = data.account.trim().split(/\s+/).slice(0, 2).map(s => Array.from(s)[0]).join('').toUpperCase();
    if (!month) month = new Date(data.today.slice(0, 7) + '-01T00:00:00Z');
    content.removeAttribute('aria-busy');
  }
  function intro(title, subtitle, action = '') {
    return '<div class="intro"><div><h1>' + title + '</h1><p>' + subtitle + '</p></div>' + action + '</div>';
  }
  function frame() { return '<iframe class="screen-frame" src="/admin/preview" title="Aperçu du diaporama"></iframe>'; }
  function noItems(title, description, action = '') {
    return '<div class="empty"><span class="empty-icon" aria-hidden="true">▧</span><h2>' + title +
      '</h2><p>' + description + '</p>' + action + '</div>';
  }
  function lowDisk() {
    return data.free < 500 * 1024 * 1024
      ? '<p class="warning" role="status">Espace disque faible. Il reste ' + bytes(data.free) + ' sur le volume.</p>' : '';
  }
  function dashboard() {
    const count = status => data.images.filter(p => p.status === status).length;
    const active = count('active');
    const upcoming = data.images.filter(p => p.status === 'scheduled').sort((a, b) => a.start.localeCompare(b.start)).slice(0, 5);
    const stats = [
      ['▣', 'En diffusion', active, 'Visuels dans la rotation'],
      ['▤', 'Programmées', count('scheduled'), 'Publications à venir'],
      ['◷', 'Expirées', count('expired'), 'Disponibles en bibliothèque'],
      ['▱', 'Stockage utilisé', bytes(data.used), bytes(data.free) + ' disponibles']
    ];
    return intro('Tableau de bord', 'Gérez les communications diffusées dans les salles.', button('Importer des visuels', 'import', 'primary plus')) + lowDisk() +
      '<div class="stats">' + stats.map(s => '<div class="stat"><div class="stat-label"><span class="stat-icon" aria-hidden="true">' + s[0] + '</span>' + s[1] +
        '</div><strong>' + s[2] + '</strong><small>' + s[3] + '</small></div>').join('') + '</div>' +
      '<div class="dashboard-grid"><section class="card"><div class="card-head"><div><h2>Diaporama actuel</h2><p>Aperçu de la diffusion sur les Airtame</p></div>' +
      '<span class="live ' + (active ? '' : 'waiting') + '">' + (active ? 'En diffusion' : 'En veille') + '</span></div>' + frame() +
      '<div class="screen-caption"><span>Format de référence 16:9</span><a href="#previsualisation">Voir la prévisualisation</a></div></section>' +
      '<section class="card"><div class="card-head"><h2>Prochaines publications</h2></div>' +
      (upcoming.length ? '<ul class="compact-list">' + upcoming.map(p => '<li><strong>' + html(p.name) + '</strong><span>' + html(localDate(p.start)) + '</span></li>').join('') +
        '</ul><a href="#programmation">Voir la programmation</a>' :
        '<div class="empty-side"><h3>Aucune publication programmée</h3><p>Les prochains visuels programmés apparaîtront ici.</p><a href="#programmation">Voir la programmation</a></div>') +
      '</section></div>';
  }
  function library() {
    const all = data.images.filter(p => p.status !== 'trash');
    const trashed = data.images.filter(p => p.status === 'trash');
    const list = inTrash ? trashed : all;
    chosen = new Set([...chosen].filter(id => list.some(p => p.id === id)));
    return intro('Bibliothèque', 'Importez, organisez et gérez vos visuels.', button('Importer des visuels', 'import', 'primary plus')) + lowDisk() +
      '<div class="toolbar"><div class="switch" aria-label="Collection">' +
      button('Visuels (' + all.length + ')', 'library', '', 'aria-pressed="' + !inTrash + '"') +
      button('Corbeille (' + trashed.length + ')', 'trash-view', '', 'aria-pressed="' + inTrash + '"') + '</div>' +
      '<div id="selection" class="row-controls" aria-live="polite">' + selectionActions() + '</div></div>' +
      '<section class="card" aria-label="' + (inTrash ? 'Corbeille' : 'Visuels') + '">' +
      (list.length ? '<div class="toolbar"><label class="check"><input type="checkbox" id="select-all" ' + (list.every(p => chosen.has(p.id)) ? 'checked' : '') +
        '>Tout sélectionner</label>' + (!inTrash ? '<span class="muted small">Déplacez les visuels pour modifier leur ordre de diffusion.</span>' : '') + '</div><div class="library-list">' +
        list.map((p, index) => row(p, index, list.length)).join('') + '</div>' :
        (inTrash ? noItems('La corbeille est vide', 'Les visuels supprimés apparaîtront ici.') : noItems('Aucun visuel', 'Ajoutez vos fichiers PNG ou JPEG pour lancer la diffusion.', button('Importer des visuels', 'import', 'primary plus')))) +
      '</section>';
  }
  function row(p, index, count) {
    const id = 'data-id="' + p.id + '"';
    return '<div class="visual-row" ' + id + '><span class="row-order">' +
      (!inTrash ? '<span class="grip" draggable="true" aria-hidden="true" title="Déplacer">⠿</span>' : '') +
      '<input class="choose" type="checkbox" aria-label="Sélectionner ' + html(p.name) + '" value="' + p.id + '" ' + (chosen.has(p.id) ? 'checked' : '') + '></span>' +
      '<img loading="lazy" alt="" src="' + media(p, true) + '"><div><h3>' + html(p.name) + '</h3><small>' + p.width + ' × ' + p.height + ' · ' + bytes(p.bytes) + '</small></div>' +
      '<span class="badge ' + p.status + '">' + labels[p.status] + '</span><div class="row-controls">' +
      (!inTrash ? '<button type="button" class="icon-button" data-action="up" ' + id + ' aria-label="Monter ' + html(p.name) + '" ' + (index === 0 ? 'disabled' : '') + '>↑</button>' +
      '<button type="button" class="icon-button" data-action="down" ' + id + ' aria-label="Descendre ' + html(p.name) + '" ' + (index === count - 1 ? 'disabled' : '') + '>↓</button>' : '') +
      button(inTrash ? 'Voir' : 'Gérer', 'details', 'secondary', id) + '</div></div>';
  }
  function selectionActions() {
    if (!chosen.size) return '';
    return '<span class="muted small">' + chosen.size + ' sélectionné' + (chosen.size === 1 ? '' : 's') + '</span>' +
      (inTrash ? button('Restaurer', 'restore-selection') + button('Supprimer définitivement', 'purge-selection', 'danger') :
        button('Mettre à la corbeille', 'trash-selection', 'danger'));
  }
  function calendar() {
    const year = month.getUTCFullYear(), m = month.getUTCMonth();
    const days = new Date(Date.UTC(year, m + 1, 0)).getUTCDate();
    const offset = (month.getUTCDay() + 6) % 7;
    const key = month.toISOString().slice(0, 7);
    const rows = Math.ceil((offset + days) / 7) * 7;
    const list = data.images.filter(p => p.status !== 'trash' && (p.start || p.end));
    const events = new Map();
    list.forEach(p => {
      [['start', 'Début'], ['end', 'Fin']].forEach(([kind, label]) => {
        const date = p[kind].slice(0, 10);
        if (!date.startsWith(key + '-')) return;
        if (!events.has(date)) events.set(date, []);
        events.get(date).push({ p, label, date: p[kind] });
      });
    });
    let cells = '';
    for (let i = 0; i < rows; i++) {
      const d = i - offset + 1;
      if (d < 1 || d > days) { cells += '<div class="day blank" aria-hidden="true"></div>'; continue; }
      const date = key + '-' + String(d).padStart(2, '0');
      cells += '<div class="day ' + (date === data.today ? 'today' : '') + '"><strong>' + d + '</strong>' +
        (events.get(date) || []).sort((a, b) => a.date.localeCompare(b.date)).map(e => {
          const title = e.label + ' à ' + e.date.slice(11) + ' · ' + e.p.name;
          return '<button data-action="details" data-id="' + e.p.id + '" title="' + html(title) + '">' + html(title) + '</button>';
        }).join('') + '</div>';
    }
    const periods = list.filter(p => (!p.start || p.start.slice(0, 7) <= key) && (!p.end || p.end.slice(0, 7) >= key));
    return intro('Programmation', 'Consultez les dates de diffusion en heure de Paris.', button('Importer des visuels', 'import', 'primary plus')) +
      '<section class="card"><div class="card-head"><h2>' + html(monthFmt.format(month)) + '</h2><div class="calendar-controls">' +
      button('Mois précédent', 'prev-month', 'secondary', 'aria-label="Mois précédent"') + button('Mois suivant', 'next-month', 'secondary', 'aria-label="Mois suivant"') +
      '</div></div><div class="calendar">' + ['Lun.', 'Mar.', 'Mer.', 'Jeu.', 'Ven.', 'Sam.', 'Dim.'].map(d => '<div class="weekday">' + d + '</div>').join('') + cells +
      '</div></section><section class="card calendar-periods"><div class="card-head"><h2>Publications sur la période</h2></div>' +
      (periods.length ? '<ul class="compact-list">' + periods.map(p => '<li><strong>' + html(p.name) + '</strong><span>' + html((p.start ? localDate(p.start) : 'Sans date de début') +
        ' — ' + (p.end ? localDate(p.end) : 'Sans date de fin')) + '</span>' + button('Modifier', 'details', 'quiet', 'data-id="' + p.id + '"') + '</li>').join('') +
        '</ul>' : '<p class="muted">Aucune publication avec une date de diffusion sur ce mois.</p>') + '</section>';
  }
  function preview() {
    return intro('Prévisualisation', 'Retrouvez le même diaporama que sur vos écrans.',
      '<a class="button primary" href="/" target="_blank" rel="noopener">Ouvrir en plein écran</a>') +
      '<section class="card" aria-label="Diaporama">' + frame() + '</section>';
  }
  function preferences() {
    const s = data.settings;
    const clock = data.clock;
    const positions = { 'top-left': 'En haut à gauche', 'top-right': 'En haut à droite',
      'bottom-left': 'En bas à gauche', 'bottom-right': 'En bas à droite' };
    const options = { fade: 'Fondu', zoom: 'Zoom lent', slide: 'Glissement', black: 'Fondu noir', none: 'Aucun effet' };
    return intro('Paramètres', 'Réglez l’affichage et gérez les sauvegardes.') + lowDisk() +
      '<div class="settings-grid"><form class="card" id="display-settings"><h2>Diffusion</h2>' +
      '<div class="control-grid"><label class="field">Affichage (s)<input required type="number" name="duration" min="5" max="120" value="' + s.duration + '"></label>' +
      '<label class="field">Transition (s)<input required type="number" name="seconds" min=".5" max="3" step=".1" value="' + s.transitionMs / 1000 + '"></label></div>' +
      '<label class="field">Effet<select name="transition">' + Object.entries(options).map(([k, name]) => '<option value="' + k + '" ' + (k === s.transition ? 'selected' : '') + '>' + name + '</option>').join('') + '</select></label>' +
      '<label class="check"><input type="checkbox" name="fallback" ' + (s.fallback ? 'checked' : '') + '>Privilégier le fondu sur les Airtame</label>' +
      '<button type="submit" class="button primary">Enregistrer</button></form>' +
      '<section class="card"><h2>Identité des écrans</h2>' +
      '<form class="stack" id="name-settings"><label class="field">Nom affiché<input required name="displayName" value="' + html(data.displayName) + '"></label>' +
      '<p class="muted small">Ce nom identifie l’application et s’affiche en veille si aucun logo n’est disponible.</p>' +
      '<button type="submit" class="button primary">Enregistrer le nom</button></form>' +
      '<form class="stack" id="logo-settings">' +
      (s.logo ? '<img class="logo-image" alt="Logo de veille" src="/media/' + encodeURIComponent(s.logo) + '">' : '<p class="muted">Ajoutez un logo PNG pour l’afficher en veille.</p>') +
      '<label class="field">Logo PNG<input required name="logo" type="file" accept="image/png,.png"></label><div class="row-controls">' +
      '<button type="submit" class="button primary">' + (s.logo ? 'Remplacer le logo' : 'Importer le logo') + '</button>' +
      (s.logo ? button('Retirer', 'remove-logo', 'danger') : '') + '</div></form></section>' +
      '<form class="card" id="clock-settings"><h2>Horloge</h2>' +
      '<label class="check"><input type="checkbox" name="enabled" ' + (clock.enabled ? 'checked' : '') + '>Afficher l’heure sur les visuels</label>' +
      '<label class="field">Position<select name="position">' + Object.entries(positions).map(([key, label]) =>
        '<option value="' + key + '" ' + (key === clock.position ? 'selected' : '') + '>' + label + '</option>').join('') + '</select></label>' +
      '<p class="muted small">Heure de Paris, au format 24 h.</p><button type="submit" class="button primary">Enregistrer l’horloge</button></form>' +
      '<section class="card"><div><h2>Stockage et sauvegardes</h2><p class="muted small">' + bytes(data.used) + ' utilisés · ' + bytes(data.free) + ' libres sur le volume</p></div>' +
      '<p class="muted">Une sauvegarde est créée chaque jour. Téléchargez une copie pour la conserver hors du VPS.</p>' +
      button('Créer une sauvegarde', 'backup') + (data.backups.length ? '<ul class="compact-list">' + data.backups.map(b =>
        '<li><a href="/api/admin/backups/' + encodeURIComponent(b.name) + '">Télécharger la sauvegarde du ' + html(dateFmt.format(new Date(b.at))) +
        '</a><span>' + bytes(b.bytes) + '</span></li>').join('') + '</ul>' : '<p class="muted small">Aucune sauvegarde disponible.</p>') + '</section>' +
      '<section class="card"><h2>Authentification</h2><p>Microsoft Entra ID · ' + html(data.account) + '</p><p class="muted small">Les comptes autorisés sont définis dans la configuration du serveur.</p></section></div>';
  }
  function render(preserveFrame = false) {
    if (!data) return;
    const oldFrame = preserveFrame && content.querySelector('.screen-frame');
    const markup = ({
      'tableau-de-bord': dashboard, bibliotheque: library,
      programmation: calendar, previsualisation: preview, parametres: preferences
    }[view])();
    if (oldFrame && view === 'tableau-de-bord') {
      const next = document.createElement('template');
      next.innerHTML = markup;
      for (const selector of ['.stats', '.live', '.dashboard-grid section:last-child:not(:first-child)']) {
        content.querySelector(selector)?.replaceWith(next.content.querySelector(selector));
      }
      const warning = content.querySelector('.warning'), nextWarning = next.content.querySelector('.warning');
      if (warning && !nextWarning) warning.remove();
      if (!warning && nextWarning) content.querySelector('.intro').after(nextWarning);
      if (warning && nextWarning) warning.replaceWith(nextWarning);
      return;
    }
    content.innerHTML = markup;
  }
  function openDialog(title, body) {
    dialog.innerHTML = '<div class="dialog-head"><h2 id="dialog-title">' + title + '</h2><button type="button" class="icon-button" data-action="close" aria-label="Fermer">×</button></div>' + body;
    if (!dialog.open) dialog.showModal();
  }
  function closesForm(label, cls = 'primary') {
    return '<p class="dialog-error error-text" role="alert"></p><div class="dialog-actions">' +
      button('Annuler', 'close', 'secondary') + '<button type="submit" class="button ' + cls + '">' + label + '</button></div>';
  }
  function dateFields(start, end, required = false) {
    return '<div class="control-grid"><label class="field start-field">Début (facultatif)<input type="datetime-local" name="start" value="' + html(start) + '" ' +
      (required ? 'required' : '') + '></label><label class="field">Fin (facultative)<input type="datetime-local" name="end" value="' + html(end) + '"></label></div>';
  }
  const accepted = '.png,.jpg,.jpeg,image/png,image/jpeg';
  function importDialog() {
    openDialog('Importer des visuels', '<form class="stack" id="import-form"><label class="dropzone" for="image-files"><strong>Glissez-déposez vos images ici</strong>' +
      '<span>ou cliquez pour choisir des PNG et JPEG</span><input id="image-files" type="file" accept="' + accepted + '" multiple></label>' +
      '<ul class="file-list" aria-live="polite"></ul><div class="upload-progress" hidden><span role="status"></span>' +
      '<progress value="0" aria-label="Progression de l’import"></progress></div><p class="muted small">25 Mio par image. Format conseillé 1920 × 1080.</p>' +
      '<label class="field">Publication<select name="mode"><option value="immediate">Publier immédiatement</option><option value="scheduled">Programmer</option>' +
      '<option value="draft">Enregistrer comme brouillon</option></select></label>' + dateFields('', '') + '<p class="muted small">Dates en heure de Paris.</p>' + closesForm('Importer') + '</form>');
    const form = document.getElementById('import-form');
    const files = new Map();
    const list = form.querySelector('.file-list');
    const dropzone = form.querySelector('.dropzone');
    const fileInput = form.querySelector('#image-files');
    const progress = form.querySelector('progress');
    const progressBox = form.querySelector('.upload-progress');
    const progressStatus = progressBox.querySelector('span');
    let dragDepth = 0;
    function fileList() {
      list.innerHTML = [...files.values()].map((item, i) => '<li><span>' + html(item.file.name) + '<small>' + html(item.error || item.detail) +
        '</small></span><button type="button" class="icon-button" aria-label="Retirer ' + html(item.file.name) + '" data-remove="' + i + '">×</button></li>').join('');
    }
    function append(fileListInput) {
      if (dialog.dataset.busy) return;
      progressBox.hidden = true;
      for (const file of fileListInput) {
        const key = [file.name, file.lastModified, file.size].join('/');
        if (files.has(key)) continue;
        const item = { file, detail: bytes(file.size) };
        files.set(key, item);
        const url = URL.createObjectURL(file);
        const image = new Image();
        image.onload = () => {
          const w = image.naturalWidth, h = image.naturalHeight;
          item.detail = bytes(file.size) + ' · ' + w + ' × ' + h + (w * 9 !== h * 16 ? ' · Format différent de 16:9' : '');
          fileList();
          URL.revokeObjectURL(url);
        };
        image.onerror = () => URL.revokeObjectURL(url);
        image.src = url;
      }
      fileList();
    }
    fileInput.addEventListener('change', e => { append(e.target.files); e.target.value = ''; });
    list.addEventListener('click', e => {
      const index = e.target.dataset.remove;
      if (index === undefined || dialog.dataset.busy) return;
      files.delete([...files.keys()][Number(index)]);
      fileList();
    });
    const mode = form.elements.mode;
    function modeChanged() {
      const field = form.querySelector('.start-field');
      field.hidden = mode.value !== 'scheduled';
      form.elements.start.required = !field.hidden;
      field.firstChild.textContent = 'Début';
    }
    mode.addEventListener('change', modeChanged);
    modeChanged();
    const carriesFiles = e => Array.from(e.dataTransfer.types).includes('Files');
    form.addEventListener('dragover', e => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = dropzone.contains(e.target) && !dialog.dataset.busy ? 'copy' : 'none';
    });
    dropzone.addEventListener('dragenter', e => {
      if (!carriesFiles(e) || dialog.dataset.busy) return;
      e.preventDefault();
      dragDepth++;
      dropzone.classList.add('over');
    });
    dropzone.addEventListener('dragleave', () => {
      dragDepth = Math.max(0, dragDepth - 1);
      if (!dragDepth) dropzone.classList.remove('over');
    });
    form.addEventListener('drop', e => {
      if (!carriesFiles(e) && !e.dataTransfer.files.length) return;
      e.preventDefault();
      dragDepth = 0;
      dropzone.classList.remove('over');
      if (dropzone.contains(e.target)) append(e.dataTransfer.files);
    });
    form.addEventListener('submit', e => {
      e.preventDefault();
      runDialog(form, async () => {
        if (!files.size) throw new Error('Sélectionnez au moins une image.');
        const params = new URLSearchParams();
        if (mode.value === 'draft') params.set('draft', '1');
        if (mode.value === 'scheduled') params.set('start', form.elements.start.value);
        if (form.elements.end.value) params.set('end', form.elements.end.value);
        let success = 0, completed = 0, index = 0;
        const count = files.size;
        const total = [...files.values()].reduce((sum, item) => sum + Math.max(1, item.file.size), 0);
        progress.max = total;
        progress.value = 0;
        progressBox.hidden = false;
        fileInput.disabled = true;
        try {
          for (const [key, item] of files) {
            const current = ++index + ' sur ' + count;
            progressStatus.textContent = 'Envoi du fichier ' + current;
            item.error = '';
            try {
              await sendFile('images?' + params, item.file, (loaded, uploaded) => {
                progress.value = completed + Math.min(loaded, item.file.size);
                if (uploaded) progressStatus.textContent = 'Validation du fichier ' + current;
              });
              files.delete(key);
              success++;
            } catch (err) { item.error = err.message; }
            completed += Math.max(1, item.file.size);
            progress.value = completed;
          }
          progressStatus.textContent = success + ' sur ' + count + ' visuels importés';
        } finally { fileInput.disabled = false; }
        if (success) {
          await refresh();
          render(true);
          showNotice(success + (success === 1 ? ' visuel importé.' : ' visuels importés.'));
        }
        if (files.size) {
          fileList();
          throw new Error('Consultez le résultat à côté de chaque fichier restant.');
        }
      });
    });
  }
  function details(p) {
    if (!p) return;
    const actions = p.status === 'trash' ? button('Restaurer', 'restore-one', 'secondary', 'data-id="' + p.id + '"') +
      button('Supprimer définitivement', 'purge-one', 'danger', 'data-id="' + p.id + '"') :
      button('Remplacer l’image', 'replace', 'secondary', 'data-id="' + p.id + '"') +
      button('Mettre à la corbeille', 'trash-one', 'danger', 'data-id="' + p.id + '"');
    openDialog(html(p.name), '<div class="stack"><img class="details-preview" alt="Aperçu du visuel" src="' + media(p) + '">' +
      '<div class="row-controls"><span class="badge ' + p.status + '">' + labels[p.status] + '</span><span class="muted small">' +
      p.width + ' × ' + p.height + ' · ' + bytes(p.bytes) + (p.width * 9 !== p.height * 16 ? ' · Format différent de 16:9' : '') + '</span></div>' +
      (p.status !== 'trash' ? '<form id="publication-form" class="stack">' + dateFields(p.start, p.end) +
        '<p class="muted small">Dates en heure de Paris.</p><label class="check"><input type="checkbox" name="enabled" ' + (p.enabled ? 'checked' : '') +
        '>Diffusion activée</label><label class="check"><input type="checkbox" name="draft" ' + (p.draft ? 'checked' : '') + '>Conserver en brouillon</label>' +
        closesForm('Enregistrer') + '</form>' : '') + '<div class="row-controls">' + actions + '</div></div>');
    const form = document.getElementById('publication-form');
    if (form) form.addEventListener('submit', e => {
      e.preventDefault();
      runDialog(form, async () => {
        const f = form.elements;
        await json('images/' + p.id, 'PATCH', { start: f.start.value, end: f.end.value, enabled: f.enabled.checked, draft: f.draft.checked });
        await updated('Publication mise à jour.');
      });
    });
  }
  function replacement(p) {
    openDialog('Remplacer l’image', '<form class="stack" id="replace-form"><p>La nouvelle image conserve les dates et la position de ce visuel.</p>' +
      '<label class="field">Nouvelle image<input name="file" type="file" accept="' + accepted + '" required></label>' + closesForm('Remplacer') + '</form>');
    const f = document.getElementById('replace-form');
    f.addEventListener('submit', e => {
      e.preventDefault();
      runDialog(f, async () => {
        await sendFile('images/' + p.id + '/replace', f.elements.file.files[0]);
        await updated('Image remplacée.');
      });
    });
  }
  async function runDialog(form, action) {
    if (dialog.dataset.busy) return;
    dialog.dataset.busy = '1';
    const error = form.querySelector('.dialog-error');
    const submit = form.querySelector('[type=submit]');
    submit.disabled = true;
    error.textContent = '';
    try { await action(); dialog.close(); }
    catch (err) { error.textContent = err.message; }
    finally { submit.disabled = false; delete dialog.dataset.busy; }
  }
  dialog.addEventListener('cancel', e => { if (dialog.dataset.busy) e.preventDefault(); });
  async function updated(message) { await refresh(); render(true); showNotice(message); }
  function confirmAction(title, text, label, action, cls = 'danger') {
    openDialog(title, '<form class="stack" id="confirmation"><p>' + text + '</p>' + closesForm(label, cls) + '</form>');
    const form = document.getElementById('confirmation');
    form.addEventListener('submit', e => { e.preventDefault(); runDialog(form, action); });
  }
  function trashAction(action, ids) {
    const wording = {
      trash: ['Mettre à la corbeille', 'Les visuels sélectionnés quittent la diffusion. Vous pourrez les restaurer.', 'Visuels mis à la corbeille.'],
      restore: ['Restaurer', 'Les visuels reprennent leur programmation et sont placés en fin de liste.', 'Visuels restaurés.'],
      purge: ['Supprimer définitivement', 'Les fichiers sélectionnés seront supprimés de la bibliothèque et de la corbeille.', 'Visuels supprimés définitivement.']
    }[action];
    confirmAction(wording[0], wording[1], wording[0], async () => {
      await json('trash', 'POST', { ids, action });
      chosen.clear();
      await updated(wording[2]);
    }, action === 'restore' ? 'primary' : 'danger');
  }
  async function move(id, to) {
    const list = data.images.filter(p => p.status !== 'trash').map(p => p.id);
    const from = list.indexOf(id);
    if (from < 0 || to < 0 || to >= list.length || from === to) return;
    list.splice(from, 1);
    list.splice(to, 0, id);
    await json('order', 'POST', { ids: list });
    await updated('Ordre de diffusion enregistré.');
    content.querySelector('[data-id="' + id + '"] button:not([disabled])')?.focus();
  }

  document.addEventListener('click', async e => {
    const target = e.target.closest('[data-action]');
    if (!target || !data || dialog.dataset.busy) return;
    const action = target.dataset.action, id = Number(target.dataset.id);
    const publication = data.images.find(p => p.id === id);
    const bulk = /^(trash|restore|purge)-(one|selection)$/.exec(action);
    try {
      if (bulk) { trashAction(bulk[1], bulk[2] === 'one' ? [id] : [...chosen]); return; }
      switch (action) {
        case 'import': importDialog(); break;
        case 'close': dialog.close(); break;
        case 'details': details(publication); break;
        case 'replace': replacement(publication); break;
        case 'library':
        case 'trash-view': inTrash = action === 'trash-view'; chosen.clear(); render(); break;
        case 'up':
        case 'down': {
          const index = data.images.filter(p => p.status !== 'trash').findIndex(p => p.id === id);
          await move(id, index + (action === 'up' ? -1 : 1));
          break;
        }
        case 'prev-month':
        case 'next-month': month.setUTCMonth(month.getUTCMonth() + (action === 'next-month' ? 1 : -1)); render(); break;
        case 'remove-logo': confirmAction('Retirer le logo', 'L’écran de veille affichera « ' + html(data.displayName) + ' ».',
          'Retirer le logo', async () => { await api('/api/admin/logo', { method: 'DELETE' }); await updated('Logo retiré.'); }); break;
        case 'backup':
          target.disabled = true;
          target.textContent = 'Sauvegarde en cours…';
          try {
            await api('/api/admin/backups', { method: 'POST' });
            await updated('Sauvegarde disponible au téléchargement.');
          } finally { target.disabled = false; target.textContent = 'Créer une sauvegarde'; }
          break;
      }
    } catch (err) { showNotice(err.message); }
  });
  content.addEventListener('change', e => {
    const el = e.target;
    if (el.id === 'select-all') {
      chosen = new Set(el.checked ? [...content.querySelectorAll('.choose')].map(c => Number(c.value)) : []);
      content.querySelectorAll('.choose').forEach(c => { c.checked = el.checked; });
    } else if (el.classList.contains('choose')) {
      if (el.checked) chosen.add(Number(el.value)); else chosen.delete(Number(el.value));
    } else return;
    const all = document.getElementById('select-all');
    all.checked = choosingAll();
    all.indeterminate = chosen.size > 0 && !all.checked;
    document.getElementById('selection').innerHTML = selectionActions();
  });
  function choosingAll() { return chosen.size === content.querySelectorAll('.choose').length; }
  content.addEventListener('dragstart', e => {
    if (!e.target.classList.contains('grip')) return;
    dragID = Number(e.target.closest('.visual-row').dataset.id);
    e.dataTransfer.setData('text/plain', String(dragID));
    e.dataTransfer.effectAllowed = 'move';
  });
  content.addEventListener('dragover', e => {
    const row = e.target.closest('.visual-row');
    if (!dragID || !row || Number(row.dataset.id) === dragID) return;
    e.preventDefault();
    content.querySelectorAll('.over').forEach(el => el.classList.remove('over'));
    row.classList.add('over');
  });
  content.addEventListener('dragend', () => {
    content.querySelectorAll('.over').forEach(el => el.classList.remove('over')); dragID = undefined;
  });
  content.addEventListener('drop', async e => {
    const row = e.target.closest('.visual-row');
    if (!dragID || !row) return;
    e.preventDefault();
    const id = dragID, to = data.images.filter(p => p.status !== 'trash').findIndex(p => p.id === Number(row.dataset.id));
    try { await move(id, to); } catch (err) { showNotice(err.message); }
  });
  content.addEventListener('submit', async e => {
    const f = e.target;
    if (f.id !== 'display-settings' && f.id !== 'logo-settings' && f.id !== 'name-settings' && f.id !== 'clock-settings') return;
    e.preventDefault();
    const submit = f.querySelector('[type=submit]');
    if (submit.disabled) return;
    submit.disabled = true;
    try {
      if (f.id === 'display-settings') {
        const fields = f.elements;
        await json('settings', 'PUT', { duration: Number(fields.duration.value), transition: fields.transition.value,
          transitionMs: Math.round(Number(fields.seconds.value) * 1000), fallback: fields.fallback.checked });
        await updated('Paramètres enregistrés.');
      } else if (f.id === 'clock-settings') {
        await json('clock', 'PUT', { enabled: f.elements.enabled.checked, position: f.elements.position.value });
        await updated('Horloge enregistrée.');
      } else if (f.id === 'name-settings') {
        await json('display-name', 'PUT', { name: f.elements.displayName.value });
        await updated('Nom affiché enregistré.');
      } else {
        await sendFile('logo', f.elements.logo.files[0]);
        await updated('Logo de veille enregistré.');
      }
    } catch (err) { showNotice(err.message); }
    finally { submit.disabled = false; }
  });

  window.addEventListener('hashchange', route);
  route();
  refresh().then(() => render()).catch(err => {
    content.removeAttribute('aria-busy');
    content.querySelector('p').textContent = err.message;
  });
  setInterval(async () => {
    if (dialog.open || dragID || view === 'parametres' || view === 'previsualisation') return;
    try {
      await refresh();
      if (!dialog.open && !dragID && view !== 'parametres' && view !== 'previsualisation') render(true);
    } catch (_) {}
  }, 60000);
}());
