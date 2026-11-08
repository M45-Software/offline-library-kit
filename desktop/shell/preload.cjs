'use strict';
const { contextBridge, ipcRenderer } = require('electron');
contextBridge.exposeInMainWorld('desktopHost', Object.freeze({
  useLibraryDrive: drive => ipcRenderer.invoke('use-library-drive', drive),
  useStageDrive: drive => ipcRenderer.invoke('use-stage-drive', drive),
  chooseLibrary: path => ipcRenderer.invoke('choose-library', path),
  setCollectionSelection: selection => ipcRenderer.invoke('set-collection-selection', selection),
  getStagingOptions: () => ipcRenderer.invoke('get-staging-options'),
  setStagingReserve: reserve => ipcRenderer.invoke('set-staging-reserve', reserve),
  setStagingSource: source => ipcRenderer.invoke('set-staging-source', source),
  setStagingStrategy: strategy => ipcRenderer.invoke('set-staging-strategy', strategy),
  chooseStageDestination: path => ipcRenderer.invoke('choose-stage-destination', path),
  getDestinationMode: () => ipcRenderer.invoke('get-destination-mode'),
  setDestinationMode: mode => ipcRenderer.invoke('set-destination-mode', mode)
}));
