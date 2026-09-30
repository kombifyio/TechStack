// Info tips of the Node step (wizard step 2). Each tip explains one choice in
// plain words; the linked guide carries the technical detail.
export const nodeGuidanceMessages = {
  en: {
    "wizard.tip.moreAbout": "More about {topic}",
    "wizard.tip.readGuide": "Read the guide",
    "wizard.tip.owned":
      "Use a computer, mini PC, home server or rented VPS you already have. kombify sets it up for your Homelab; the device stays yours.",
    "wizard.tip.new":
      "No device yet? kombify can provide a server for you, or you rent one from a partner and connect it afterwards.",
    "wizard.tip.installCommand":
      "The easiest path. After the wizard you get a personal command. Paste it into the terminal of your device and kombify connects it and shows the installation progress.",
    "wizard.tip.connectRemote":
      "For a server you reach over the network. You enter its address and sign-in once, and kombify runs the setup for you over SSH.",
    "wizard.tip.managed":
      "kombify prepares a virtual server at one of its infrastructure partners and connects it to your Homelab. Which providers you see depends on your account.",
    "wizard.tip.partner":
      "Rent a server directly from a partner. When it is ready, come back and connect it like any device you own.",
    "wizard.tip.systemUbuntu":
      "The standard for most devices. The installer checks the operating system before it changes anything.",
    "wizard.tip.systemProxmox":
      "Choose this when the device runs Proxmox. kombify leaves the host as it is and creates a separate Ubuntu VM for your tools.",
    "wizard.tip.remoteHost":
      "The name or IP address under which this device answers, for example 192.168.1.20 or server.example.com.",
    "wizard.tip.authKey":
      "Recommended. kombify signs in with an SSH key you saved in your Wallet, so no password has to be typed in here.",
    "wizard.tip.authPassword":
      "Signs in with the device’s SSH password. Your browser does not keep it; kombify stores it encrypted in your Wallet so the setup can be retried.",
    "wizard.tip.keyLabel":
      "The name you gave the key when you saved it in your Wallet. kombify uses that key to sign in to the device.",
    "wizard.tip.sshDetails":
      "Only needed when your device differs from the defaults: another user than root, another SSH port than 22, or setup commands that need sudo.",
    "wizard.tip.testConnection":
      "Checks now whether kombify can reach and sign in to the device, before anything is installed.",
    "wizard.tip.managedProvider":
      "The company whose data center runs your server. kombify handles the account, the server and its cleanup with that provider.",
    "wizard.tip.nodeJoin":
      "The device joins this StackKit and gives it more computing power or storage. Access, people and sign-in stay as they are.",
    "wizard.tip.nodeFound":
      "Starts a separate StackKit with this device as its main Node, for example a second site or a cloud part of your Homelab.",
    "wizard.tip.nodeSubstrate":
      "Registers a Proxmox host itself. kombify can then create and manage VMs on it for this Homelab.",
    "wizard.tip.foundationBasement":
      "For hardware you control: at home, in your office, or a server you rent yourself.",
    "wizard.tip.foundationCloud":
      "For a virtual server that kombify provides and manages for you.",
    "wizard.tip.foundationLocked":
      "A Node that joins a StackKit always uses that StackKit’s foundation, so there is nothing to choose here.",
    "wizard.tip.roleFoundation":
      "The first and central Node of a StackKit. It runs the core services the other Nodes rely on.",
    "wizard.tip.roleWorker":
      "An additional Node that takes over services and spreads the load of your StackKit.",
    "wizard.tip.roleStorage":
      "An additional Node meant for services that need a lot of disk space, such as photos, media or backups.",
    "wizard.tip.hypervisorResources":
      "Storage is the Proxmox storage where the VM disk is created. The LAN bridge connects the VM to your home network.",
    "wizard.tip.hypervisorSize":
      "CPU cores, memory and disk space reserved for the Ubuntu VM. The fields do not go below what the Ubuntu VM needs to run.",
  },
  de: {
    "wizard.tip.moreAbout": "Mehr zu {topic}",
    "wizard.tip.readGuide": "Zur Anleitung",
    "wizard.tip.owned":
      "Nutze einen Computer, Mini-PC, Homeserver oder gemieteten VPS, den du schon hast. kombify richtet ihn für dein Homelab ein; das Gerät bleibt deins.",
    "wizard.tip.new":
      "Noch kein Gerät? kombify kann dir einen Server bereitstellen, oder du mietest einen bei einem Partner und verbindest ihn danach.",
    "wizard.tip.installCommand":
      "Der einfachste Weg. Nach dem Wizard bekommst du einen persönlichen Befehl. Füge ihn im Terminal deines Geräts ein, dann verbindet kombify es und zeigt den Fortschritt der Installation.",
    "wizard.tip.connectRemote":
      "Für einen Server, den du über das Netzwerk erreichst. Du gibst Adresse und Anmeldung einmal ein, und kombify übernimmt die Einrichtung per SSH.",
    "wizard.tip.managed":
      "kombify bereitet einen virtuellen Server bei einem Infrastruktur-Partner vor und verbindet ihn mit deinem Homelab. Welche Anbieter du siehst, hängt von deinem Konto ab.",
    "wizard.tip.partner":
      "Miete einen Server direkt bei einem Partner. Wenn er bereit ist, komm zurück und verbinde ihn wie ein eigenes Gerät.",
    "wizard.tip.systemUbuntu":
      "Der Standard für die meisten Geräte. Der Installer prüft das Betriebssystem, bevor er etwas verändert.",
    "wizard.tip.systemProxmox":
      "Wähle das, wenn auf dem Gerät Proxmox läuft. kombify lässt den Host unverändert und legt eine eigene Ubuntu-VM für deine Tools an.",
    "wizard.tip.remoteHost":
      "Der Name oder die IP-Adresse, unter der das Gerät erreichbar ist, zum Beispiel 192.168.1.20 oder server.example.com.",
    "wizard.tip.authKey":
      "Empfohlen. kombify meldet sich mit einem SSH-Schlüssel aus deinem Wallet an, du musst hier kein Passwort eingeben.",
    "wizard.tip.authPassword":
      "Meldet sich mit dem SSH-Passwort des Geräts an. Dein Browser speichert es nicht; kombify legt es verschlüsselt in deinem Wallet ab, damit die Einrichtung wiederholt werden kann.",
    "wizard.tip.keyLabel":
      "Der Name, den du dem Schlüssel beim Speichern im Wallet gegeben hast. Mit diesem Schlüssel meldet sich kombify am Gerät an.",
    "wizard.tip.sshDetails":
      "Nur nötig, wenn dein Gerät von den Standardwerten abweicht: ein anderer Benutzer als root, ein anderer SSH-Port als 22 oder Einrichtungsbefehle, die sudo brauchen.",
    "wizard.tip.testConnection":
      "Prüft sofort, ob kombify das Gerät erreicht und sich anmelden kann, bevor etwas installiert wird.",
    "wizard.tip.managedProvider":
      "Das Unternehmen, in dessen Rechenzentrum dein Server läuft. kombify kümmert sich bei diesem Anbieter um Konto, Server und Aufräumen.",
    "wizard.tip.nodeJoin":
      "Das Gerät tritt diesem StackKit bei und bringt mehr Rechenleistung oder Speicher. Zugang, Personen und Anmeldung bleiben, wie sie sind.",
    "wizard.tip.nodeFound":
      "Startet ein eigenes StackKit mit diesem Gerät als Haupt-Node, zum Beispiel für einen zweiten Standort oder einen Cloud-Teil deines Homelabs.",
    "wizard.tip.nodeSubstrate":
      "Registriert einen Proxmox-Host selbst. kombify kann dann VMs darauf für dieses Homelab anlegen und verwalten.",
    "wizard.tip.foundationBasement":
      "Für Hardware, über die du selbst verfügst: zu Hause, im Büro oder ein Server, den du selbst mietest.",
    "wizard.tip.foundationCloud":
      "Für einen virtuellen Server, den kombify für dich bereitstellt und verwaltet.",
    "wizard.tip.foundationLocked":
      "Ein Node, der einem StackKit beitritt, nutzt immer dessen Grundlage. Hier gibt es daher nichts auszuwählen.",
    "wizard.tip.roleFoundation":
      "Der erste und zentrale Node eines StackKits. Er betreibt die Kerndienste, auf die sich die anderen Nodes stützen.",
    "wizard.tip.roleWorker":
      "Ein zusätzlicher Node, der Dienste übernimmt und die Last deines StackKits verteilt.",
    "wizard.tip.roleStorage":
      "Ein zusätzlicher Node für Dienste mit viel Speicherbedarf, etwa Fotos, Medien oder Backups.",
    "wizard.tip.hypervisorResources":
      "Storage ist der Proxmox-Speicher, auf dem die VM-Festplatte angelegt wird. Die LAN-Bridge verbindet die VM mit deinem Heimnetz.",
    "wizard.tip.hypervisorSize":
      "CPU-Kerne, Arbeitsspeicher und Festplatte, die für die Ubuntu-VM reserviert werden. Die Felder gehen nicht unter das, was die Ubuntu-VM zum Laufen braucht.",
  },
} as const;
