from sandbox import Sandbox

sb = Sandbox(base_url="http://localhost:8080")

# single shot - no state
r = sb.run("print('hello from single shot')")
print(r.output)

# session - state persists
with sb.session() as sess:
    sess.run("x = 10")
    sess.run("y = 20")
    r = sess.run("print(x + y)")
    print(r.output)  # 30
