# Nimbus Backup - Client Proxmox Backup Server

[🇬🇧 English](README.md) · **🇫🇷 Français**

[![Licence](https://img.shields.io/badge/license-GPLv3-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/rdemsystems/NimbusBackupClient)](https://github.com/rdemsystems/NimbusBackupClient/releases)
[![Documentation](https://img.shields.io/badge/docs-nimbus.rdem--systems.com-orange)](https://nimbus.rdem-systems.com/)

Client de sauvegarde Windows moderne pour Proxmox Backup Server, avec une interface graphique intuitive.

## 🌐 RDEM Systems

- **Site web** : https://www.rdem-systems.com/
- **Nimbus Backup — hébergement PBS entièrement infogéré** : https://nimbus.rdem-systems.com/
- **Support** : contact@rdem-systems.com

Vous ne voulez pas héberger PBS vous-même ? Utilisez notre service infogéré :
👉 [NimbusBackup — PBS infogéré en France](https://nimbus.rdem-systems.com/?utm_source=github&utm_campaign=gui-client)

- ✅ À partir de 12 €/To/mois
- ✅ 1 To d'essai gratuit

## 📦 Téléchargement

👉 **[Télécharger la dernière version](https://github.com/rdemsystems/NimbusBackupClient/releases)**

> ⚠️ **Windows affiche « virus détecté » (ex. `Trojan:Win32/Sabsik.FL.A!ml`) ou un avertissement SmartScreen ?**
> C'est un **faux positif** connu pour les applications Go/Wails — ce n'est *pas* un virus.
> Le suffixe `!ml` indique une détection par un modèle de machine learning qui signale les
> exécutables *non signés et peu répandus* ; aucun moteur sur VirusTotal ne signale ces fichiers.
> Lisez [pourquoi cela arrive et comment vérifier que le téléchargement est sain](https://nimbus.rdem-systems.com/faux-positif-antivirus/)
> ([🇬🇧 English version](https://nimbus.rdem-systems.com/en/antivirus-false-positive/)).

**Vérifier n'importe quel téléchargement** — chaque release fournit des empreintes SHA-256 et une
attestation de provenance signée (preuve cryptographique que le binaire a été produit par la CI de
ce dépôt, à partir de ce commit) :

```powershell
Get-FileHash .\NimbusBackup.exe -Algorithm SHA256   # comparer avec SHA256SUMS.txt
gh attestation verify .\NimbusBackup.exe --repo rdemsystems/NimbusBackupClient
```

> ℹ️ **Signature de code :** les binaires Windows ne sont **pas encore signés Authenticode**
> (un certificat OSS via [SignPath Foundation](https://signpath.org) est en attente).
> En attendant, la provenance est établie via l'attestation et les empreintes ci-dessus.

## 📚 Documentation

- **Guide complet de sauvegarde Proxmox** — bonnes pratiques de déploiement PBS ([🇫🇷 FR](https://nimbus.rdem-systems.com/blog/guide-complet-backup-proxmox/?utm_source=github&utm_campaign=gui-client) / [🇬🇧 EN](https://nimbus.rdem-systems.com/en/blog/complete-proxmox-backup-guide/?utm_source=github&utm_campaign=gui-client))
- **Sauvegarder Windows avec Proxmox Backup Server** — guide de déploiement spécifique Windows ([🇫🇷 FR](https://nimbus.rdem-systems.com/blog/sauvegarder-windows-proxmox-backup-server/?utm_source=github&utm_campaign=gui-client) / [🇬🇧 EN](https://nimbus.rdem-systems.com/en/blog/backup-windows-proxmox-backup-server/?utm_source=github&utm_campaign=gui-client))

## ✨ Fonctionnalités

### Interface graphique (recommandée)
- **🌍 Multilingue** — interface en français et en anglais
- Configuration conviviale avec test de connexion
- Progression de sauvegarde en temps réel avec débit et temps restant
- Support VSS (Volume Shadow Copy) pour des sauvegardes cohérentes
- Sauvegarde multi-dossiers
- Navigation dans les snapshots et restauration
- Détection automatique du nom d'hôte
- Journalisation de débogage pour le diagnostic

### 📸 Captures d'écran

![Configuration des serveurs](docs/screenshots/nimbus-gui-liste-servers.png)
*Gestion multi-serveurs PBS avec indicateurs d'état*

![Formulaire d'ajout de serveur](docs/screenshots/nimbus-gui-add-server-form.png)
*Configuration de serveur simple avec test de connexion*

![Sauvegarde immédiate](docs/screenshots/nimbus-gui-one-shot-backup.png)
*Progression de sauvegarde en temps réel avec ETA et débit*

### Sécurité & qualité
- Validation des entrées et nettoyage des identifiants
- Prévention des traversées de chemin (path traversal)
- Logique de réessai avec backoff exponentiel
- Gestion d'erreurs complète
- Conformité lint à 100 %

### Exclusions système intelligentes (mode fichier)
Lors de la sauvegarde d'un disque entier (ex. `D:\`), Nimbus Backup exclut automatiquement :

**Dossiers système :**
- `System Volume Information` — stockage des snapshots VSS (peut atteindre 100+ Go)
- `$RECYCLE.BIN` — corbeille Windows
- `Recovery` — données de la partition de récupération Windows

**Fichiers système :**
- `pagefile.sys` — fichier d'échange Windows
- `hiberfil.sys` — fichier de mise en veille prolongée
- `swapfile.sys` — fichier de swap Windows

**Pourquoi c'est important :**
- Un disque affiche 1,03 To utilisés mais les fichiers réels font 141 Go
- Sans exclusions, la sauvegarde inclurait les snapshots VSS (espace et temps gaspillés)
- Avec exclusions, la taille de la sauvegarde correspond aux données réelles (~141 Go)

**Recommandation :**
- **Sauvegardes au niveau fichier** (par défaut) : utilisez le mode fichier avec auto-exclusions
- **Restauration bare-metal** : utilisez le mode disque dans une tâche séparée (inclut tout)

## 🚀 Démarrage rapide

1. Téléchargez `NimbusBackup.exe` depuis les releases
2. Lancez-le avec les droits administrateur (requis pour VSS)
3. Configurez votre connexion PBS
4. Testez la connexion
5. Sélectionnez les dossiers à sauvegarder
6. Lancez la sauvegarde

## 📋 Prérequis

- Windows 10/11 (64 bits)
- Droits administrateur (pour les snapshots VSS)
- Accès réseau au serveur PBS

## ⚠️ Avertissement

Ce logiciel est fourni « tel quel ». Bien que nous visions la fiabilité, nous déclinons toute responsabilité en cas de perte ou de dommage de données.
Testez toujours vos sauvegardes et vérifiez la restauration avant de vous y fier en production.

## 🔮 Feuille de route

### Priorité haute
- Chiffrement côté client avec gestion des clés
- Signature de code (certificat Authenticode)
- Système de mise à jour automatique
- Icône dans la zone de notification et service en arrière-plan
- Sauvegardes planifiées (quotidiennes, hebdomadaires, personnalisées)
- Mode service Windows

### Futur
- Limitation de bande passante
- Compression multi-cœurs
- Notifications toast Windows

## 🔨 Compilation depuis les sources

### Prérequis
- Go 1.22 ou ultérieur
- Node.js 20 ou ultérieur
- Wails CLI : `go install github.com/wailsapp/wails/v2/cmd/wails@latest`

### Commandes de build
```bash
# Compiler la GUI
cd gui
npm install --prefix frontend
wails build

# Lancer en mode dev (rechargement à chaud)
wails dev
```

## 📝 Projet d'origine

Ce projet est un fork de [tizbac/proxmoxbackupclient_go](https://github.com/tizbac/proxmoxbackupclient_go), enrichi d'une interface graphique moderne et de fonctionnalités supplémentaires pour les utilisateurs Windows.

**Projet original** : Proxmox Backup Client en Go
**Auteur** : Tiziano Bacocco (tizbac)
**Licence** : GPLv3

Principaux ajouts dans ce fork :
- Interface GUI Wails v2 avec frontend React
- Suivi de progression en temps réel
- Gestion d'erreurs et journalisation améliorées
- Durcissement de la sécurité
- Tests complets
- Pipelines CI/CD

### Comparaison des fonctionnalités

| Fonctionnalité            | tizbac/proxmoxbackupclient_go | NimbusBackupClient (ce fork) |
|---------------------------|:-----------------------------:|:----------------------------:|
| Mode CLI                  | ✅                             | ✅                            |
| GUI Wails                 | ❌                             | ✅                            |
| Multilingue (FR/EN)       | ❌                             | ✅                            |
| Progression en temps réel | ❌                             | ✅                            |
| Exclusions système        | ❌                             | ✅                            |
| Pipelines CI/CD           | ❌                             | ✅                            |
| Tests complets            | ❌                             | ✅                            |

## 📄 Licence

GPLv3 — voir le fichier LICENSE

## 🤝 Contribuer

Les contributions sont les bienvenues ! Domaines prioritaires :
1. 🔐 Chiffrement côté client
2. 🔄 Mécanisme de mise à jour automatique
3. 📅 Sauvegardes planifiées
4. 🔒 Signature de code

## À propos de RDEM Systems

NimbusBackupClient est développé et maintenu par [RDEM Systems](https://www.rdem-systems.com/), un fournisseur d'infrastructure français spécialisé dans l'infogérance Proxmox VE/PBS et l'infrastructure NTP/NTS.

Nous exploitons [11 serveurs NTS publics](https://github.com/jauderho/nts-servers) listés dans la référence communautaire, et proposons un [hébergement PBS entièrement infogéré](https://nimbus.rdem-systems.com/?utm_source=github&utm_campaign=gui-client-about) pour ceux qui ne veulent pas auto-héberger.

---

**© 2024-2026 RDEM Systems. Tous droits réservés.**
