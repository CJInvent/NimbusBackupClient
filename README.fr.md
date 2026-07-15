# Nimbus Backup

Agent de sauvegarde Windows pour **Proxmox Backup Server** (PBS), pensé pour
le déploiement MSP : un couple GUI native + service Windows qui sauvegarde des
répertoires ou des disques entiers vers PBS, restaure au fichier près depuis
les deux types de sauvegarde, et remonte vers
[NimbusControl](https://github.com/CJInvent/NimbusControl) pour la
supervision de parc.

*Version française abrégée — la référence complète est [README.md](README.md).*

## L'essentiel

- **Sauvegarde** : répertoires (pxar) et **volumes entiers** (image brute via
  VSS), planifiée ou immédiate ; limite de bande passante montante ; multi-PBS.
- **Navigation dans les images disque sans les restaurer** : partitions GPT/MBR,
  NTFS / FAT12-16-32 / exFAT lues en espace utilisateur ; table des fichiers
  téléchargée par plan exact (insensible à la fragmentation) ; rien n'est
  caché ; tri par nom, date, taille.
- **Un seul flux de restauration** pour les deux types : sélection, options,
  un bouton. Les options inapplicables sont grisées avec la raison en
  infobulle — permissions NTFS et flux ADS **lus directement dans l'image**
  pour les sauvegardes de volumes NTFS (BÊTA), grisés pour FAT/exFAT (le
  format ne les stocke pas) et pour les sauvegardes de répertoires (capture
  sidecar planifiée). Limite de bande passante descendante partagée.

## Construction

```
cd gui/frontend && npm ci && npm run build
cd .. && go build .                # GUI
go build -tags service .          # service
```

Détails d'architecture : [ARCHITECTURE.md](ARCHITECTURE.md) ·
Module de lecture d'images : [imagebrowse/README.md](imagebrowse/README.md)
