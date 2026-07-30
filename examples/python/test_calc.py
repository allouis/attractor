import unittest

from calc import add, greeting


class CalcTest(unittest.TestCase):
    def test_add(self):
        self.assertEqual(add(2, 3), 5)

    def test_greeting(self):
        self.assertEqual(greeting("attractor"), "hello, attractor")


if __name__ == "__main__":
    unittest.main()
