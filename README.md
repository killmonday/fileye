# Fileye

## 介绍

本项目是为了Trovelocal（典藏）项目而建立，作用是运行在SMB服务器上监听SMB目录的文件/文件夹的创建、更新、重命名、删除等事件，并使用http api推送需要处理的事件到Trovelocal后台，以便Trovelocal服务端能正确同步文件的更新、删除、重命名。项目使用了fsnotify库（基于inotify），实现了目录和子目录的动态监听，当目录下有新建文件夹或者重命名的文件夹时，这些变化的文件夹将自动加入监听。功能包括：

- 可以**指定监听的目录**
- 可以**指定推送的http服务端地址**
- 对于文件内容的修改，会监听CloseWrite事件，以避免大量的Write事件影响性能，且CloseWrite事件是一次写入完成后才会触发，接近于文件内容保存成功的时刻。为什么这里说“接近”，因为像office word等文档编辑器，当你CTRL+S保存时，其实会创建两个临时文件，把修改写入其中一个，另一个似乎用来备份，如果一切成功的话，会把某个临时文件remove到你的真实文档，这个过程中出现了数次的CloseWrite事件（非常坑）。所以我使用一个map来记录每个CloseWrite事件的信息，当5秒钟内同一个文件没有产生新的CloseWrite事件时才会push到Trovelocal的服务端，然后从map中移除以避免内存无限增长。这样就解决了这类编辑器在编辑和保存文件时多次触发CloseWrite事件的问题。
- 对于文件夹重命名，我们会延迟最多500毫秒再添加到watcher中。fsnotify的官方文档都没有提及一个坑点，那就是事件触发时，操作系统也许并没有真正处理完这个操作，比如文件夹的创建也许在100毫秒后才创建成功，在文件夹没有创建之前，添加到watcher是不会成功的，遗憾的是fsnotify对于不存在的文件夹的添加没有任何异常和报错，排查这个问题卡了我半天，逼得我直接打印wacher列表看看什么情况，最后灵光一闪写了个延迟就成功了。
- 支持使用**文件前缀、文件后缀、正则表达式**来**排除不需要推送的文件/文件夹**，例如office办公软件产生的~$.docx、xxxxxx.tmp等临时文件、vim产生的.swp临时文件、vscode的.vscode目录、git产生的.git目录等这些不需要推送到Trovelocal的文件/文件夹。你只需要根据自己的需要修改项目目录下的prefix.txt、suffix.txt、reg.txt然后重新编译。

## 编译

go build -ldflags="-s -w" -trimpath

## 使用

```
./smbhandle -h

Usage of ./smbhandle:
  -p string
    	watch dir
  -s string
    	django server api url
```

