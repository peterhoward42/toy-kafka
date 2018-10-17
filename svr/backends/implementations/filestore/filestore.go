// Package filestore provides a message storage system based on mounted file
// system. It implements the backingstore.contract.BackingStore interface.
package filestore

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"sync"
	"time"

	toykafka "github.com/peterhoward42/toy-kafka"
	"github.com/peterhoward42/toy-kafka/svr/backends/implementations/filestore/index"
	"github.com/peterhoward42/toy-kafka/svr/backends/implementations/filestore/filenamer"
)

const maximumFileSize = 1048576 // 1 MiB

var mutex = &sync.Mutex{} // Guards concurrent access of the FileStore.

// FileStore encapsulates the store.
type FileStore struct {
    rootDir string
}

// ------------------------------------------------------------------------
// METHODS TO SATISFY THE BackingStore INTERFACE.
// ------------------------------------------------------------------------

// DeleteContents removes all contents from the store.
func (s FileStore) DeleteContents() error {
	mutex.Lock()
	defer mutex.Unlock()
	return s.deleteContents()
}

// Store is defined by, and documented in the backends/contract/BackingStore
// interface.
func (s FileStore) Store(topic string, message toykafka.Message) (
	messageNumber int, err error) {

	mutex.Lock()
	defer mutex.Unlock()
	return s.store(topic, message)
}

// RemoveOldMessages is defined by, and documented in the
// backends/contract/BackingStore interface.
func (s FileStore) RemoveOldMessages(maxAge time.Time) (
	nRemoved int, err error) {
	return -1, nil
}

// Poll is defined by, and documented in the backends/contract/BackingStore
// interface.
func (s FileStore) Poll(topic string, readFrom int) (
	foundMessages []toykafka.Message, newReadFrom int, err error) {

	foundMessages = []toykafka.Message{}
	return foundMessages, 11, nil
}

// ------------------------------------------------------------------------
// Helper functions.
// ------------------------------------------------------------------------

func (s FileStore) deleteContents() error {
	dir, err := ioutil.ReadDir(s.rootDir)
	if err != nil {
		return fmt.Errorf("ioutil.ReadDir(): %v", err)
	}
	for _, entry := range dir {
		fullpath := path.Join(s.rootDir, entry.Name())
		err = os.RemoveAll(fullpath)
		if err != nil {
			return fmt.Errorf("os.RemoveAll(): %v", err)
		}
	}
	return nil
}

func (s FileStore) store(topic string, message toykafka.Message) (
	messageNumber int, err error) {

	index, err := s.retrieveIndexFromDisk()
	if err != nil {
		return -1, fmt.Errorf("RetrieveIndexFromDisk(): %v", err)
	}
	err = s.createTopicDirIfNotExists(topic)
	if err != nil {
		return -1, fmt.Errorf("createTopicDirIfNotExists: %v", err)
	}
	msgNumber := index.NextMessageNumberFor(topic)
	msgToStore := s.makeMsgToStore(message, msgNumber)
	msgSize := len(msgToStore)

    var msgFileName string
    msgFileName = index.CurrentMsgFileNameFor(topic)
    var needNewFile = false
    if msgFileName == "" {
        needNewFile = true
    } else {
        needNewFile, err = s.fileHasInsufficentRoom(
            msgFileName, topic, msgSize)
        if err != nil {
            return -1, fmt.Errorf("fileHasInsufficietRoom(): %v", err)
        }
    }
    if needNewFile {
        msgFileName, err = s.setupNewFileForTopic(topic, index)
        if err != nil {
            return -1, fmt.Errorf("setupNewFileForTopic(): %v", err)
        }
    }
	err = s.saveAndRegisterMessage(
            msgFileName, topic, msgToStore, msgNumber, index)
	if err != nil {
		return -1, fmt.Errorf("saveAndRegisterMessage(): %v", err)
	}
	err = s.saveIndex(index)
	if err != nil {
		return -1, fmt.Errorf("saveIndex(): %v", err)
	}
	return int(msgNumber), nil
}

func (s FileStore) retrieveIndexFromDisk() (*index.Index, error) {
    indexPath := filenamer.IndexFile(s.rootDir)
    file, err := os.Open(indexPath)
    if err != nil {
        return nil, fmt.Errorf("os.Open(): %v", err)
    }
    defer file.Close()
    index := index.NewIndex()
    err = index.Decode(file)
    if err != nil {
        return nil, fmt.Errorf("index.Decode(): %v", err)
    }
    return index, nil
}

func (s *FileStore) saveIndex(index *index.Index) error {
    indexPath := filenamer.IndexFile(s.rootDir)
    file, err := os.Open(indexPath)
    if err != nil {
        return fmt.Errorf("os.Open(): %v", err)
    }
    defer file.Close()
    err = index.Encode(file)
    if err != nil {
        return fmt.Errorf("index.Encode(): %v", err)
    }
    return nil
}

func (s FileStore) makeMsgToStore(
	message toykafka.Message, msgNumber int32) []byte {
	msg := storedMessage{message, time.Now(), msgNumber}
	var buf bytes.Buffer
	encoder := gob.NewEncoder(&buf)
	encoder.Encode(msg)
	return buf.Bytes()
}

func (s FileStore) createTopicDirIfNotExists(topic string) error {
	dirPath := filenamer.DirectoryForTopic(topic, s.rootDir)
	err := os.Mkdir(dirPath, 0777) // Todo what should permissions be?
	if err == nil {
		return nil
	}
	if os.IsExist(err) {
		return nil
	}
	return fmt.Errorf("os.Mkdir(): %v", err)
}

func (s FileStore) fileHasInsufficentRoom(
    msgFileName string, topic string, msgSize int) (bool, error) {
    filepath := filenamer.MessageFilePath(msgFileName, topic, s.rootDir)
    file, err := os.Open(filepath)
    if err != nil {
        return false, fmt.Errorf("os.Open(): %v", err)
    }
    defer file.Close()
    fileInfo, err := file.Stat()
    if err != nil {
        return false, fmt.Errorf("file.Stat(): %v", err)
    }
    size := fileInfo.Size()
    insufficient := size + int64(msgSize) > maximumFileSize
    return insufficient, nil
}

func (s FileStore) setupNewFileForTopic(
    topic string, index *index.Index) (msgFileName string, err error) {
    fileName := filenamer.NewMsgFilenameFor(topic, index)
    filepath := filenamer.MessageFilePath(fileName, topic, s.rootDir)
    file, err := os.Create(filepath)
    if err != nil {
        return false, fmt.Errorf("os.Create(): %v", err)
    }
    defer file.Close()
    msgFileList := index.GetMessageFileListFor(topic)
    msgFileList.RegisterNewFile(fileName) 
    return fileName, nil
}

func (s FileStore) saveAndRegisterMessage(
    msgFileName string, topic string, msgToStore []byte, 
    msgNumber int32, index *index.Index) err {
    filepath := filenamer.MessageFilePath(msgFileName, topic, s.rootDir)
    file, err := os.OpenFile(filePath, os.O_APPEND, 0666)
    if err != nil {
        return fmt.Errorf("os.OpenFile(): %v", err)
    }
    defer file.Close()
    something, err := file.Write(msgToStore)
    if err != nil {
        return fmt.Errorf("file.Write(): %v", err)
    }
    creationTime := time.Now()
    msgFileList := index.GetMessageFileListFor(topic)
    fileMeta := msgFileList.Meta[msgFileName]
    fileMeta.RegisterNewMessage(msgNumber, creationTime)
    return nil
    }


// ------------------------------------------------------------------------
// Auxilliary types.
// ------------------------------------------------------------------------

type storedMessage struct {
	message       toykafka.Message
	creationTime  time.Time
	messageNumber int32
}