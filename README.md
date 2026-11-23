# Saint André Écrans

Application de gestion et de diffusion des communications de Saint André. Le conteneur Go embarque l’interface HTML, CSS et JavaScript. SQLite et les fichiers vivent dans un volume Docker. Caddy fournit l’accès HTTPS.

| Adresse | Usage |
| --- | --- |
| https://ecrans.saint-andre.tech/ | Diaporama public à ouvrir sur chaque Airtame |
| https://ecrans.saint-andre.tech/admin | Administration avec Microsoft 365 |
| https://ecrans.saint-andre.tech/healthz | Contrôle de disponibilité |

Le domaine ci-dessus est celui retenu dans le cahier des charges. Pour un essai, utilisez un autre sous-domaine HTTPS et adaptez le DNS, l’hôte déclaré dans Caddy, l’origine configurée dans l’application et l’URI de redirection dans Entra.

## Microsoft Entra ID

1. Dans le [portail Microsoft Entra](https://entra.microsoft.com), ouvrez **Inscriptions d’applications**, puis créez une inscription **Saint André Écrans**. Sélectionnez les comptes de cet annuaire uniquement.
2. Ajoutez une plateforme **Web** avec l’URI de redirection exacte **https://ecrans.saint-andre.tech/auth/callback**. Le flux utilise un code d’autorisation avec PKCE. L’application demande les scopes OIDC **openid**, **profile** et **email**. Elle n’appelle pas Microsoft Graph. L’émission de jetons pour le flux implicite peut rester désactivée.
3. Relevez l’**ID de l’annuaire (locataire)** et l’**ID d’application (client)**. Dans **Certificats et secrets**, créez un secret client et copiez sa **valeur** dans le fichier de configuration sur le VPS. Renouvelez le secret avant son expiration.
4. Dans **Utilisateurs**, relevez l’**ID d’objet** du compte administrateur. Pour plusieurs comptes autorisés, séparez leurs ID par des virgules. L’identification repose sur le tenant et l’Object ID.

Les cookies de session sont sécurisés et valables huit heures. Un redémarrage impose une nouvelle connexion à l’administration. Le diaporama continue sans session.

## Installation

Le VPS doit disposer de Docker Compose v2 et de Caddy. Faites pointer le DNS du domaine vers le VPS qui reçoit les connexions HTTPS. Le service rejoint le réseau Docker existant de Caddy et ne publie aucun port hôte.

Les fichiers `compose.yaml`, `.env.example` et `Caddyfile.example` suffisent pour le déploiement. Récupérez-les depuis le dépôt GitHub privé. Dans ce dossier sur le VPS, créez **.env** depuis **.env.example** avec des permissions privées.

~~~sh
umask 077
cp .env.example .env
~~~

| Variable | Valeur |
| --- | --- |
| PUBLIC_URL | Origine HTTPS, par exemple https://ecrans.saint-andre.tech |
| ENTRA_TENANT_ID | ID de l’annuaire Saint André |
| ENTRA_CLIENT_ID | ID de l’inscription d’application |
| ENTRA_CLIENT_SECRET | Valeur du secret client |
| ENTRA_ADMIN_OBJECT_IDS | ID d’objet autorisés, séparés par des virgules |
| ECRANS_IMAGE | Image à télécharger. Valeur par défaut **ghcr.io/babgond/saint-andre-ecrans:1.0.3** |
| CADDY_NETWORK | Nom du réseau Docker déjà utilisé par Caddy. Valeur par défaut **caddy** |
| ECRANS_NETWORK_ALIAS | Alias du conteneur sur le réseau Caddy. Valeur par défaut **saint-andre-ecrans** |
| ECRANS_DATA_VOLUME | Nom du volume Docker. Valeur par défaut **saint-andre-ecrans-data** |
| BACKUP_KEEP | Nombre d’archives à garder, manuelles et automatiques comprises. Valeur par défaut **7** |

Si Caddy utilise un proxy HTTP sortant, ajoutez **ECRANS_NETWORK_ALIAS** à sa variable **NO_PROXY** pour atteindre directement le service Docker.

L’image privée est disponible pour les VPS Linux AMD64 et ARM64. Connectez Docker au registre avec un compte GitHub qui a accès au paquet. À l’invite du mot de passe, saisissez un [jeton d’accès personnel classique](https://github.com/settings/tokens) avec la permission `read:packages`.

~~~sh
docker login ghcr.io -u babgond
~~~

Copiez le bloc du **Caddyfile.example** dans la configuration existante de Caddy. Téléchargez l’image et démarrez le service, puis rechargez Caddy.

~~~sh
docker compose config --quiet
docker compose pull
docker compose up -d
docker compose ps
curl -fsS https://ecrans.saint-andre.tech/healthz
~~~

En cas de test sur le même VPS que la production, donnez à l’instance de test un volume, un projet Compose et un alias réseau distincts et reportez l’alias dans le bloc Caddy. Utilisez une URI de redirection supplémentaire dans Entra si le domaine diffère. Gardez un conteneur applicatif par volume.

## Publier des visuels

- Ouvrez **Bibliothèque** puis **Importer des visuels**. Sélectionnez ou déposez vos PNG et JPEG. L’import publie immédiatement par défaut. Vous pouvez choisir une date de début ou un brouillon et ajouter une date de fin.
- Le format recommandé est **1920 × 1080**. Les autres ratios sont signalés et s’affichent dans leurs proportions. Les fichiers sont limités à 25 Mio, 24 mégapixels et 10 000 pixels par côté.
- Dans **Gérer**, modifiez les dates, désactivez un visuel ou remplacez son original. Pour republier un visuel expiré, retirez ou repoussez sa date de fin. Utilisez la poignée ou les boutons de déplacement pour définir l’ordre.
- Dans **Paramètres**, modifiez le **Nom affiché** dans **Identité des écrans**. « Saint André » est proposé par défaut. Ce nom s’affiche aussi en veille, et vous pouvez ajouter un logo PNG pour l’y remplacer. La durée de chaque visuel est réglable de 5 à 120 secondes et celle des transitions de 0,5 à 3 secondes. Le réglage « Privilégier le fondu sur les Airtame » remplace les autres effets animés par un fondu.
- Les dates sont saisies en heure de Paris. Une heure sautée au printemps est refusée. Une heure répétée en automne désigne la première occurrence. Le serveur recalcule l’éligibilité à chaque consultation du diaporama. Les Airtame vérifient la liste toutes les 60 secondes et basculent à la fin du visuel en cours si la rotation continue.

Un visuel expiré reste en bibliothèque. La corbeille conserve l’original jusqu’à la suppression définitive. Les nouveaux visuels et ceux restaurés sont ajoutés à la fin de la liste.

## Sauvegarder et restaurer

Une archive est produite au démarrage si aucune sauvegarde n’existe pour la date du jour en heure de Paris, puis lors du premier contrôle de la journée. Le contrôle a lieu toutes les 30 minutes. L’archive contient une copie cohérente de SQLite, les images originales, les miniatures et le logo. **Paramètres** permet de déclencher ou télécharger une sauvegarde. Gardez des copies hors du VPS et conservez séparément la configuration Caddy et le fichier **.env**, qui contient le secret Entra.

Pour tester une restauration, utilisez un nouveau volume Docker. Fournissez le chemin d’une archive téléchargée dans la variable ci-dessous.

~~~sh
archive=/chemin/vers/ecrans-archive.tar.gz
volume=saint-andre-ecrans-restauration
docker run --rm -i --network none -v "$volume:/data" \
  ghcr.io/babgond/saint-andre-ecrans:1.0.3 restore /dev/stdin /data < "$archive"
~~~

La commande vérifie SQLite et la présence des fichiers référencés. Elle exige un volume vide. Pour basculer vers le volume restauré, arrêtez le service avec **docker compose stop ecrans**, réglez **ECRANS_DATA_VOLUME** sur le nouveau nom et relancez avec **docker compose up -d**. Conservez l’ancien volume jusqu’à la fin de la vérification. Pour mettre à jour l’application, prenez une sauvegarde, puis sélectionnez le nouveau tag avec **ECRANS_IMAGE** dans **.env**. Relancez **docker compose pull** et **docker compose up -d** en réutilisant le même volume. Vérifiez la santé du service avant de reprendre la diffusion.

## Recette sur le VPS et les Airtame

1. Déployez sur une URL de test et connectez-vous avec le compte Saint André autorisé. Vérifiez le refus avec un compte absent de la liste. Dans un navigateur sans session, ouvrez le diaporama.
2. Importez plusieurs exports Canva, dont un JPEG et un PNG 16:9. Vérifiez les miniatures, les doublons, les changements d’ordre, la corbeille et la restauration.
3. Sur un premier Airtame, pointez vers l’URL HTTPS. Testez une publication pendant la diffusion, une programmation de courte durée, une expiration et l’affichage du logo de veille. Comparez les transitions et observez la fluidité et la mémoire pendant une rotation prolongée.
4. Redémarrez le conteneur. Vérifiez que la diffusion reprend et que les programmations sont conservées. Testez le téléchargement d’une sauvegarde et sa restauration sur un autre volume.
5. Récupérez les exports de WordPress, réimportez-les et reconstituez dates et ordre selon les besoins. Après validation sur l’Airtame de test, changez l’URL des autres appareils. Conservez Foyer accessible durant la période de fonctionnement parallèle.

## Vérifications de développement

Go 1.26 sur Linux suffit pour compiler et tester. Les tests automatisés couvrent les routes protégées, le flux OIDC avec un fournisseur de test, la programmation, les changements d’heure, la bibliothèque, les sauvegardes et la restauration.

~~~sh
go test -v ./...
go vet ./...
docker build -t saint-andre-ecrans:dev .
~~~

Un tag Git au format `vMAJEUR.MINEUR.CORRECTIF` déclenche les tests et publie la version correspondante sur GitHub Container Registry.
