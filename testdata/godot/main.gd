extends Label

func _process(_delta):
	text = "EasyDist Godot Web\nThreaded export is running\nElapsed: %.1f seconds" % (Time.get_ticks_msec() / 1000.0)
